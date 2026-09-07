"""Codex adapter backed by ``codex app-server`` or the optional Python SDK."""
from __future__ import annotations

import importlib
import json
import logging
import os
import queue
import shutil
import subprocess
import sys
import threading
import time
from functools import lru_cache
from pathlib import Path
from typing import Optional

from django.conf import settings

from ..messages import format_messages_for_query
from ..models import LLMResponse, TokenUsage
from .base import (
    AgentAdapter,
    AgentEvent,
    EventType,
    OperationStatus,
    ProgressCategory,
)

logger = logging.getLogger(__name__)


class _ProtocolObject:
    """Expose app-server's camelCase JSON fields as Python attributes."""

    def __init__(self, payload: dict) -> None:
        self._payload = payload

    def __getattr__(self, name):
        camel_name = name.split("_")[0] + "".join(
            part.title() for part in name.split("_")[1:]
        )
        for candidate in (name, camel_name):
            if candidate in self._payload:
                return _protocol_value(self._payload[candidate])
        raise AttributeError(name)

    def model_dump(self, **_options) -> dict:
        return self._payload


def _protocol_value(value):
    if isinstance(value, dict):
        return _ProtocolObject(value)
    if isinstance(value, list):
        return [_protocol_value(item) for item in value]
    return value


class _AppServerTransport:
    """Minimal JSONL client for a dedicated Codex app-server process."""

    def __init__(self, *, codex_bin: Path) -> None:
        creation_flags = 0
        if sys.platform == "win32":
            creation_flags = getattr(subprocess, "CREATE_NO_WINDOW", 0)
        self._process = subprocess.Popen(
            [str(codex_bin), "app-server", "--stdio"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            errors="replace",
            bufsize=1,
            creationflags=creation_flags,
        )
        self._request_id = 0
        self._request_lock = threading.Lock()
        self._pending: dict[int, queue.Queue] = {}
        self._notifications: queue.Queue = queue.Queue()
        self._stderr_lines: list[str] = []
        self._reader = threading.Thread(target=self._read_stdout, daemon=True)
        self._stderr_reader = threading.Thread(target=self._read_stderr, daemon=True)
        self._reader.start()
        self._stderr_reader.start()
        try:
            self.request(
                "initialize",
                {
                    "clientInfo": {
                        "name": "creation_agent_studio",
                        "title": "Creation Agent Studio",
                        "version": "1.0.0",
                    },
                    "capabilities": None,
                },
            )
            self.notify("initialized")
        except Exception:
            self.close()
            raise

    def _read_stdout(self) -> None:
        try:
            assert self._process.stdout is not None
            for raw_line in self._process.stdout:
                raw_line = raw_line.strip()
                if not raw_line:
                    continue
                try:
                    message = json.loads(raw_line)
                except json.JSONDecodeError:
                    logger.warning("Ignoring invalid Codex app-server output: %s", raw_line)
                    continue
                request_id = message.get("id")
                if request_id is not None and "method" not in message:
                    pending = self._pending.get(request_id)
                    if pending is not None:
                        pending.put(message)
                    continue
                if request_id is not None:
                    self._handle_server_request(message)
                    continue
                if message.get("method"):
                    self._notifications.put(message)
        finally:
            detail = self._stderr_detail()
            failure = RuntimeError(
                "Codex app-server closed unexpectedly"
                + (f": {detail}" if detail else "")
            )
            for pending in list(self._pending.values()):
                pending.put(failure)
            self._notifications.put(failure)

    def _read_stderr(self) -> None:
        assert self._process.stderr is not None
        for line in self._process.stderr:
            line = line.strip()
            if line:
                self._stderr_lines.append(line)
                del self._stderr_lines[:-20]

    def _stderr_detail(self) -> str:
        return " | ".join(self._stderr_lines[-3:])

    def _send(self, message: dict) -> None:
        if self._process.poll() is not None:
            detail = self._stderr_detail()
            raise RuntimeError(
                "Codex app-server is not running" + (f": {detail}" if detail else "")
            )
        assert self._process.stdin is not None
        self._process.stdin.write(json.dumps(message, ensure_ascii=False) + "\n")
        self._process.stdin.flush()

    def request(self, method: str, params: Optional[dict] = None) -> dict:
        with self._request_lock:
            self._request_id += 1
            request_id = self._request_id
            response_queue: queue.Queue = queue.Queue(maxsize=1)
            self._pending[request_id] = response_queue
            try:
                self._send({"id": request_id, "method": method, "params": params})
                timeout = float(getattr(settings, "CODEX_REQUEST_TIMEOUT_SECONDS", 30))
                try:
                    response = response_queue.get(timeout=timeout)
                except queue.Empty as exc:
                    raise RuntimeError(
                        f"Codex app-server request timed out: {method}"
                    ) from exc
            finally:
                self._pending.pop(request_id, None)
        if isinstance(response, Exception):
            raise response
        if response.get("error"):
            error = response["error"]
            message = error.get("message", str(error)) if isinstance(error, dict) else str(error)
            raise RuntimeError(f"Codex app-server {method} failed: {message}")
        return response.get("result") or {}

    def notify(self, method: str, params: Optional[dict] = None) -> None:
        message = {"method": method}
        if params is not None:
            message["params"] = params
        self._send(message)

    def _handle_server_request(self, message: dict) -> None:
        """Decline unexpected approvals instead of leaving the turn blocked."""
        method = message.get("method", "")
        if method in {
            "item/commandExecution/requestApproval",
            "item/fileChange/requestApproval",
        }:
            self._send({"id": message["id"], "result": {"decision": "decline"}})
            return
        self._send(
            {
                "id": message["id"],
                "error": {
                    "code": -32601,
                    "message": f"Unsupported app-server request: {method}",
                },
            }
        )

    def next_notification(self, timeout: Optional[float] = None) -> dict:
        message = self._notifications.get(timeout=timeout)
        if isinstance(message, Exception):
            raise message
        return message

    def close(self) -> None:
        if self._process.poll() is not None:
            return
        if self._process.stdin is not None:
            try:
                self._process.stdin.close()
            except OSError:
                pass
        self._process.terminate()
        try:
            self._process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            self._process.kill()
            self._process.wait(timeout=2)


def _text_input(text: str) -> list[dict]:
    return [{"type": "text", "text": text, "text_elements": []}]


def _approval_settings(approval_mode: str) -> tuple[str, str]:
    if approval_mode == "auto_review":
        return "on-request", "auto_review"
    return "never", "user"


class _AppServerTurn:
    def __init__(self, *, transport: _AppServerTransport, thread_id: str, text: str, model: str):
        self._transport = transport
        self._thread_id = thread_id
        self._text = text
        self._model = model
        self.id = ""

    def _start(self) -> None:
        if self.id:
            return
        params = {"threadId": self._thread_id, "input": _text_input(self._text)}
        if self._model:
            params["model"] = self._model
        result = self._transport.request("turn/start", params)
        self.id = result["turn"]["id"]

    def stream(self):
        self._start()
        while True:
            message = self._transport.next_notification()
            params = message.get("params") or {}
            if params.get("threadId") not in (None, self._thread_id):
                continue
            notification = _ProtocolObject(
                {"method": message["method"], "payload": params}
            )
            yield notification
            if (
                message["method"] == "turn/completed"
                and params.get("turn", {}).get("id") == self.id
            ):
                return

    def steer(self, text: str) -> None:
        self._start()
        self._transport.request(
            "turn/steer",
            {
                "threadId": self._thread_id,
                "expectedTurnId": self.id,
                "input": _text_input(text),
            },
        )

    def interrupt(self) -> None:
        if self.id:
            self._transport.request(
                "turn/interrupt", {"threadId": self._thread_id, "turnId": self.id}
            )


class _AppServerThread:
    def __init__(self, *, transport: _AppServerTransport, thread_id: str) -> None:
        self._transport = transport
        self.id = thread_id

    def turn(self, text: str, *, model: Optional[str] = None) -> _AppServerTurn:
        return _AppServerTurn(
            transport=self._transport,
            thread_id=self.id,
            text=text,
            model=model or "",
        )

    def run(self, text: str, *, model: Optional[str] = None):
        turn = self.turn(text, model=model)
        chunks: list[str] = []
        completed_items: dict[str, str] = {}
        usage = None
        terminal_turn = None
        for notification in turn.stream():
            if notification.method == "item/agentMessage/delta":
                chunks.append(notification.payload.delta or "")
            elif notification.method == "item/completed":
                item = notification.payload.item
                if getattr(item, "type", "") == "agentMessage":
                    completed_items[item.id] = item.text or ""
            elif notification.method == "thread/tokenUsage/updated":
                usage = notification.payload.token_usage
            elif notification.method == "turn/completed":
                terminal_turn = notification.payload.turn
        final_response = "".join(chunks)
        if not final_response and completed_items:
            final_response = list(completed_items.values())[-1]
        terminal_turn = terminal_turn or _ProtocolObject(
            {"status": "failed", "error": {"message": "Missing turn completion"}}
        )
        return _ProtocolObject(
            {
                "status": terminal_turn.status,
                "final_response": final_response,
                "usage": usage.model_dump() if usage is not None else None,
                "error": (
                    terminal_turn.error.model_dump()
                    if getattr(terminal_turn, "error", None) is not None
                    else None
                ),
            }
        )


class _AppServerCodex:
    def __init__(self, *, codex_bin: Path, cwd: str = "") -> None:
        self._transport = _AppServerTransport(codex_bin=codex_bin)
        self._cwd = cwd

    def thread_start(
        self,
        *,
        approval_mode: str,
        base_instructions: Optional[str],
        cwd: str,
        model: Optional[str],
        sandbox: str,
    ) -> _AppServerThread:
        approval_policy, approvals_reviewer = _approval_settings(approval_mode)
        params = {
            "cwd": cwd or self._cwd,
            "approvalPolicy": approval_policy,
            "approvalsReviewer": approvals_reviewer,
            "sandbox": sandbox,
            "baseInstructions": base_instructions,
            "model": model,
        }
        params = {
            key: value for key, value in params.items() if value not in (None, "")
        }
        result = self._transport.request("thread/start", params)
        return _AppServerThread(
            transport=self._transport,
            thread_id=result["thread"]["id"],
        )

    def close(self) -> None:
        self._transport.close()


class _AppServerSdk:
    Sandbox = _ProtocolObject(
        {
            "read_only": "read-only",
            "workspace_write": "workspace-write",
            "full_access": "danger-full-access",
        }
    )
    ApprovalMode = _ProtocolObject(
        {"auto_review": "auto_review", "deny_all": "deny_all"}
    )


@lru_cache(maxsize=1)
def load_codex_sdk():
    """Load ``openai_codex`` from the configured local repository checkout."""
    sdk_path = Path(settings.CODEX_SDK_PATH).expanduser().resolve()
    package_path = sdk_path / "openai_codex"
    if not package_path.is_dir():
        raise RuntimeError(
            f"Codex Python SDK not found at {package_path}. "
            "Set CODEX_REPOSITORY_PATH or CODEX_SDK_PATH to the Codex checkout."
        )
    sdk_path_text = str(sdk_path)
    if sdk_path_text not in sys.path:
        sys.path.insert(0, sdk_path_text)
    return importlib.import_module("openai_codex")


def resolve_codex_binary() -> Path:
    """Resolve an explicit, source-built, or installed Codex binary."""
    configured = str(settings.CODEX_BINARY).strip()
    if configured:
        path = Path(configured).expanduser().resolve()
        if not path.is_file():
            raise RuntimeError(f"Configured CODEX_BINARY does not exist: {path}")
        return path

    root = Path(settings.CODEX_REPOSITORY_PATH).expanduser().resolve()
    binary_name = "codex.exe" if sys.platform == "win32" else "codex"
    for profile in ("release", "debug"):
        candidate = root / "codex-rs" / "target" / profile / binary_name
        if candidate.is_file():
            return candidate

    # The Windows standalone installer keeps versioned binaries outside the
    # conventional PATH locations used by non-interactive service processes.
    # Prefer the real executable over an npm .cmd shim for app-server stdio.
    if sys.platform == "win32":
        local_app_data = os.environ.get("LOCALAPPDATA", "").strip()
        if local_app_data:
            install_root = Path(local_app_data) / "OpenAI" / "Codex" / "bin"
            candidates = [
                candidate
                for candidate in install_root.glob(f"*/{binary_name}")
                if candidate.is_file()
            ]
            direct_candidate = install_root / binary_name
            if direct_candidate.is_file():
                candidates.append(direct_candidate)
            if candidates:
                return max(candidates, key=lambda candidate: candidate.stat().st_mtime).resolve()

    discovered = shutil.which("codex")
    if discovered:
        return Path(discovered).resolve()

    raise RuntimeError(
        "Codex executable was not found. Install the Codex App or CLI, add "
        "codex to PATH, or set CODEX_BINARY."
    )


def _enum_value(value) -> str:
    return str(getattr(value, "value", value) or "")


def _json_value(value) -> str:
    if value is None:
        return ""
    if hasattr(value, "model_dump"):
        value = value.model_dump(mode="json", by_alias=True, exclude_none=True)
    if isinstance(value, str):
        return value
    return json.dumps(value, ensure_ascii=False, default=str)


def _usage_payload(token_usage) -> dict:
    if token_usage is None:
        return {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
    last = getattr(token_usage, "last", token_usage)
    return {
        "prompt_tokens": int(getattr(last, "input_tokens", 0) or 0),
        "completion_tokens": int(getattr(last, "output_tokens", 0) or 0),
        "total_tokens": int(getattr(last, "total_tokens", 0) or 0),
    }


def _sandbox(sdk):
    configured = settings.CODEX_SANDBOX.strip().lower()
    values = {
        "read-only": sdk.Sandbox.read_only,
        "workspace-write": sdk.Sandbox.workspace_write,
        "danger-full-access": sdk.Sandbox.full_access,
        "full-access": sdk.Sandbox.full_access,
    }
    try:
        return values[configured]
    except KeyError as exc:
        raise ValueError(
            "CODEX_SANDBOX must be read-only, workspace-write, or danger-full-access"
        ) from exc


def _approval_mode(sdk):
    configured = settings.CODEX_APPROVAL_MODE.strip().lower()
    values = {
        "auto_review": sdk.ApprovalMode.auto_review,
        "deny_all": sdk.ApprovalMode.deny_all,
    }
    try:
        return values[configured]
    except KeyError as exc:
        raise ValueError("CODEX_APPROVAL_MODE must be auto_review or deny_all") from exc


class CodexSession:
    """Bridge a Codex thread's push stream to the runtime polling contract."""

    adapter_name = "codex"
    content_mode = "delta"

    def __init__(self, *, key: str, client, thread, model: str) -> None:
        self.key = key
        self._client = client
        self._thread = thread
        self._model = model
        self.agent_id = thread.id
        self.created_at = time.monotonic()
        self.last_used_at = self.created_at
        self.stream_lock = threading.Lock()
        self.configuration_key: tuple = ()
        self.is_new = True
        self._events: queue.Queue[AgentEvent] = queue.Queue()
        self._turn = None
        self._worker: Optional[threading.Thread] = None
        self._usage: Optional[dict] = None
        self._item_content: dict[str, str] = {}

    def submit(
        self,
        text: str,
        *,
        system_prompt: str = "",
        preload_skills: Optional[list[str]] = None,
    ) -> None:
        if self._worker is not None and self._worker.is_alive():
            raise RuntimeError("This Codex session already has an active turn")
        skill_hint = ""
        if preload_skills:
            skill_hint = "Selected skills: " + ", ".join(preload_skills) + "\n\n"
        self._item_content = {}
        self._usage = None
        self._turn = self._thread.turn(skill_hint + text, model=self._model or None)
        self._worker = threading.Thread(target=self._consume_turn, daemon=True)
        self._worker.start()
        self.last_used_at = time.monotonic()

    def _consume_turn(self) -> None:
        terminal_seen = False
        try:
            for notification in self._turn.stream():
                event = self._normalize_notification(notification)
                if event is None:
                    continue
                if event.type in (EventType.COMPLETED, EventType.FAILED, EventType.CANCELLED):
                    terminal_seen = True
                self._events.put(event)
        except Exception as exc:
            logger.exception("Codex turn failed for session %s", self.key)
            self._events.put(
                AgentEvent(
                    type=EventType.FAILED,
                    agent_id=self.agent_id,
                    error_code="codex_error",
                    error_message=str(exc),
                )
            )
        finally:
            if not terminal_seen:
                self._events.put(
                    AgentEvent(
                        type=EventType.FAILED,
                        agent_id=self.agent_id,
                        error_code="stream_closed",
                        error_message="Codex event stream closed before completion",
                    )
                )

    def _normalize_notification(self, notification) -> Optional[AgentEvent]:
        method = notification.method
        payload = notification.payload
        if method == "item/agentMessage/delta":
            delta = payload.delta or ""
            item_id = getattr(payload, "item_id", "")
            self._item_content[item_id] = self._item_content.get(item_id, "") + delta
            return AgentEvent(
                type=EventType.PROGRESS,
                agent_id=self.agent_id,
                content=delta,
                progress_category=ProgressCategory.CONTENT,
                query_id=getattr(payload, "turn_id", ""),
                request_id=item_id,
            )
        if method == "thread/tokenUsage/updated":
            self._usage = _usage_payload(payload.token_usage)
            return None
        if method in ("item/started", "item/completed"):
            item = getattr(payload.item, "root", payload.item)
            item_type = getattr(item, "type", "")
            if item_type == "agentMessage" and method == "item/completed":
                final_text = getattr(item, "text", "") or ""
                item_id = getattr(item, "id", "")
                if final_text and not self._item_content.get(item_id):
                    self._item_content[item_id] = final_text
                    return AgentEvent(
                        type=EventType.PROGRESS,
                        agent_id=self.agent_id,
                        content=final_text,
                        progress_category=ProgressCategory.CONTENT,
                        query_id=getattr(payload, "turn_id", ""),
                        request_id=getattr(item, "id", ""),
                    )
                return None
            tool_call = self._tool_call(item, started=method == "item/started")
            if tool_call is not None:
                return AgentEvent(
                    type=(EventType.TOOL_START if method == "item/started" else EventType.TOOL_END),
                    agent_id=self.agent_id,
                    query_id=getattr(payload, "turn_id", ""),
                    request_id=getattr(item, "id", ""),
                    tool_call=tool_call,
                )
            return None
        if method == "turn/completed":
            turn = payload.turn
            status = _enum_value(turn.status)
            if status == "completed":
                return AgentEvent(
                    type=EventType.COMPLETED,
                    agent_id=self.agent_id,
                    query_id=turn.id,
                    usage=self._usage or _usage_payload(None),
                )
            if status == "interrupted":
                return AgentEvent(
                    type=EventType.CANCELLED,
                    agent_id=self.agent_id,
                    query_id=turn.id,
                )
            error = getattr(turn, "error", None)
            return AgentEvent(
                type=EventType.FAILED,
                agent_id=self.agent_id,
                query_id=turn.id,
                error_code="codex_turn_failed",
                error_message=getattr(error, "message", "") or f"Codex turn {status}",
            )
        return None

    def _tool_call(self, item, *, started: bool) -> Optional[dict]:
        item_type = getattr(item, "type", "")
        if item_type == "commandExecution":
            return {
                "id": item.id,
                "name": "command",
                "input": _json_value({"command": item.command, "cwd": str(item.cwd)}),
                "result": "" if started else (item.aggregated_output or ""),
                "status": "running" if started else _enum_value(item.status),
                "error_message": "",
            }
        if item_type == "fileChange":
            return {
                "id": item.id,
                "name": "apply_patch",
                "input": _json_value(item.changes),
                "result": "" if started else _enum_value(item.status),
                "status": "running" if started else _enum_value(item.status),
                "error_message": "",
            }
        if item_type in ("mcpToolCall", "dynamicToolCall"):
            name = getattr(item, "tool", item_type)
            server = getattr(item, "server", "")
            if server:
                name = f"{server}.{name}"
            error = getattr(item, "error", None)
            return {
                "id": item.id,
                "name": name,
                "input": _json_value(getattr(item, "arguments", None)),
                "result": "" if started else _json_value(getattr(item, "result", None)),
                "status": "running" if started else _enum_value(item.status),
                "error_message": _json_value(error),
            }
        if item_type == "webSearch":
            return {
                "id": item.id,
                "name": "web_search",
                "input": _json_value(getattr(item, "query", "")),
                "result": "",
                "status": "running" if started else "completed",
                "error_message": "",
            }
        return None

    def wait_for_event(self, timeout: float = 15.0) -> Optional[AgentEvent]:
        try:
            event = self._events.get(timeout=timeout)
        except queue.Empty:
            return None
        self.last_used_at = time.monotonic()
        return event

    def resume(self, *, text: str = "", selections: Optional[list[str]] = None):
        if self._turn is None:
            raise RuntimeError("No active Codex turn to resume")
        values = selections or []
        response = text or (", ".join(values) if values else "Continue")
        self._turn.steer(response)
        self.last_used_at = time.monotonic()
        return OperationStatus("accepted")

    def cancel(self):
        if self._turn is None or self._worker is None or not self._worker.is_alive():
            return OperationStatus("not_running")
        self._turn.interrupt()
        self.last_used_at = time.monotonic()
        return OperationStatus("accepted")

    def close(self) -> None:
        try:
            self.cancel()
        except Exception:
            logger.debug("Codex cancellation failed during close", exc_info=True)
        if self._worker is not None and self._worker.is_alive():
            self._worker.join(timeout=2)
        self._client.close()


class CodexAdapter(AgentAdapter):
    name = "codex"
    content_mode = "delta"

    def __init__(self, *, model=None, **_options) -> None:
        self.model = model or settings.CODEX_MODEL

    def _client(self, *, cwd: str = ""):
        transport = str(getattr(settings, "CODEX_TRANSPORT", "app-server")).strip().lower()
        if transport == "app-server":
            return _AppServerSdk, _AppServerCodex(
                codex_bin=resolve_codex_binary(),
                cwd=cwd or settings.CODEX_WORKING_DIRECTORY,
            )
        if transport != "python-sdk":
            raise ValueError("CODEX_TRANSPORT must be app-server or python-sdk")
        sdk = load_codex_sdk()
        config = sdk.CodexConfig(
            codex_bin=str(resolve_codex_binary()),
            cwd=cwd or settings.CODEX_WORKING_DIRECTORY,
            client_name="creation_agent_studio",
            client_title="Creation Agent Studio",
        )
        return sdk, sdk.Codex(config=config)

    def complete(self, messages: list[dict], **options) -> LLMResponse:
        system_prompt, query = format_messages_for_query(messages)
        cwd = options.get("working_directory", "") or settings.CODEX_WORKING_DIRECTORY
        sdk, client = self._client(cwd=cwd)
        try:
            thread = client.thread_start(
                approval_mode=_approval_mode(sdk),
                base_instructions=system_prompt or None,
                cwd=cwd,
                model=self.model or None,
                sandbox=_sandbox(sdk),
            )
            result = thread.run(query, model=self.model or None)
        finally:
            client.close()
        usage = _usage_payload(result.usage)
        success = _enum_value(result.status) == "completed"
        return LLMResponse(
            content=result.final_response or "",
            usage=TokenUsage(**usage),
            model=self.model or "codex-default",
            success=success,
            error=(getattr(result.error, "message", None) if result.error else None),
        )

    def create_session(
        self,
        key: str,
        *,
        system_prompt: str,
        working_directory: str = "",
        enable_permissions: Optional[bool] = None,
        options: Optional[dict] = None,
    ) -> CodexSession:
        options = options or {}
        model = options.get("model") or self.model
        cwd = working_directory or settings.CODEX_WORKING_DIRECTORY
        sdk, client = self._client(cwd=cwd)
        try:
            thread = client.thread_start(
                approval_mode=(
                    sdk.ApprovalMode.deny_all
                    if enable_permissions
                    else (
                        sdk.ApprovalMode.auto_review
                        if enable_permissions is False
                        else _approval_mode(sdk)
                    )
                ),
                base_instructions=system_prompt or None,
                cwd=cwd,
                model=model or None,
                sandbox=_sandbox(sdk),
            )
        except Exception:
            client.close()
            raise
        return CodexSession(key=key, client=client, thread=thread, model=model)
