"""GraphFlow implementation of the agent adapter contract."""
from __future__ import annotations

import json
import logging
import threading
import time
from dataclasses import dataclass
from typing import Optional

from django.conf import settings

from ..messages import format_messages_for_query
from ..models import LLMResponse, TokenUsage
from ..sdk_loader import load_sdk
from .base import (
    AgentAdapter,
    AgentEvent,
    EventType,
    OperationStatus,
    ProgressCategory,
)

logger = logging.getLogger(__name__)

_ASK_USER_SCHEMA_GUIDANCE = """
When calling AskUserQuestion, pass one flat JSON object with: question (string),
header (short string), options (2-4 objects with label and description), and
multi_select (boolean). Do not wrap the object in a questions array.
""".strip()


def build_config(
    *,
    system_prompt: str = "",
    working_directory: str = "",
    enable_permissions: Optional[bool] = None,
    provider_override: Optional[dict] = None,
):
    sdk = load_sdk()
    guarded_system_prompt = f"{system_prompt}\n\n{_ASK_USER_SCHEMA_GUIDANCE}".strip()
    effective_permissions = (
        settings.GRAPHFLOW_ENABLE_PERMISSIONS
        if enable_permissions is None
        else enable_permissions
    )
    provider_override = provider_override or {}
    return sdk.EngineConfig(
        default_provider=provider_override.get("provider") or settings.GRAPHFLOW_PROVIDER,
        llm_model=provider_override.get("model") or settings.GRAPHFLOW_MODEL,
        llm_base_url=provider_override.get("base_url") or settings.GRAPHFLOW_BASE_URL,
        workflow_config_file=settings.GRAPHFLOW_WORKFLOW_PATH,
        enable_streaming=settings.GRAPHFLOW_ENABLE_STREAMING,
        enable_permissions=effective_permissions,
        enable_skills=settings.GRAPHFLOW_ENABLE_SKILLS,
        skills_directory=settings.GRAPHFLOW_SKILLS_DIRECTORY,
        system_prompt=guarded_system_prompt,
        working_directory=working_directory,
        max_turns=settings.GRAPHFLOW_MAX_TURNS,
        timeout_seconds=settings.GRAPHFLOW_TIMEOUT_SECONDS,
        fake_provider=settings.GRAPHFLOW_PROVIDER == "fake",
    )


def normalize_graphflow_event(event) -> AgentEvent:
    """Convert a native GraphFlow event to the shared event model."""
    sdk = load_sdk()
    event_type = event.type
    normalized = AgentEvent(
        type=event_type,
        agent_id=event.agent_id,
        content=event.content or "",
        progress_category=event.progress_category or "",
        query_id=event.query_id or "",
        request_id=event.request_id or "",
    )
    if (
        event_type == sdk.EventType.PROGRESS.value
        and event.progress_category == sdk.ProgressCategory.SKILL.value
        and event.content
    ):
        try:
            detail = json.loads(event.content)
            if detail.get("event") == "skills_loaded":
                normalized.extras.update(
                    loaded_skills=detail.get("skills", []),
                    missing_skills=detail.get("missing", []),
                )
        except (TypeError, ValueError, AttributeError):
            pass
    if event_type == sdk.EventType.QUESTION.value and event.question is not None:
        normalized.question = {
            "header": event.question.header,
            "question": event.question.question,
            "kind": "question",
            "options": [
                {
                    "label": option.label,
                    "value": option.value or option.label,
                    "description": option.description,
                }
                for option in event.question.options
            ],
        }
    if event_type == sdk.EventType.PERMISSION.value and event.permission is not None:
        tool = event.permission.tool_call
        details = event.permission.reason or f"Allow tool '{tool.name}' to run?"
        if tool.input:
            details += f"\nTool: {tool.name}\nInput: {tool.input}"
        normalized.question = {
            "header": "Permission",
            "question": details,
            "kind": "permission",
            "options": [
                {"label": "Allow", "value": "allow", "description": "Run this tool call"},
                {"label": "Deny", "value": "deny", "description": "Do not run this tool call"},
            ],
        }
    if event_type in (
        sdk.EventType.TOOL_START.value,
        sdk.EventType.TOOL_END.value,
    ) and event.tool_call is not None:
        tool = event.tool_call
        normalized.tool_call = {
            "id": tool.id,
            "name": tool.name,
            "input": tool.input,
            "result": tool.result,
            "status": "running" if event_type == sdk.EventType.TOOL_START.value else tool.status,
            "error_message": tool.error_message,
        }
    if event_type == sdk.EventType.COMPLETED.value and event.usage is not None:
        normalized.usage = {
            "prompt_tokens": event.usage.prompt_tokens,
            "completion_tokens": event.usage.completion_tokens,
            "total_tokens": event.usage.total_tokens,
        }
    if event_type == sdk.EventType.FAILED.value:
        normalized.error_code = event.error_code or ""
        normalized.error_message = event.error_message or ""
    return normalized


@dataclass
class GraphFlowSession:
    key: str
    manager: object
    agent_id: str
    created_at: float
    last_used_at: float
    stream_lock: threading.Lock
    configuration_key: tuple = ()
    is_new: bool = True
    adapter_name: str = "graphflow"
    content_mode: str = "delta"

    def submit(
        self,
        text: str,
        *,
        system_prompt: str = "",
        preload_skills: Optional[list[str]] = None,
    ) -> None:
        sdk = load_sdk()
        status = self.manager.submit(
            self.agent_id,
            text,
            system_prompt=system_prompt,
            preload_skills=preload_skills or [],
        )
        if status != sdk.SubmitStatus.ACCEPTED:
            raise RuntimeError(f"GraphFlow submit rejected: {status.value}")
        self.last_used_at = time.monotonic()

    def wait_for_event(self, timeout: float = 15.0):
        event = self.manager.wait_for_event(timeout=timeout)
        self.last_used_at = time.monotonic()
        return normalize_graphflow_event(event) if event is not None else None

    def resume(self, *, text: str = "", selections: Optional[list[str]] = None):
        sdk = load_sdk()
        status = self.manager.resume(self.agent_id, text=text, selections=selections or None)
        if status != sdk.ResumeStatus.ACCEPTED:
            raise RuntimeError(f"GraphFlow resume rejected: {status.value}")
        self.last_used_at = time.monotonic()
        return OperationStatus(status.value)

    def cancel(self):
        sdk = load_sdk()
        status = self.manager.cancel(self.agent_id)
        if status not in (sdk.CancelStatus.ACCEPTED, sdk.CancelStatus.NOT_RUNNING):
            raise RuntimeError(f"GraphFlow cancel rejected: {status.value}")
        self.last_used_at = time.monotonic()
        return OperationStatus(status.value)

    def close(self) -> None:
        try:
            self.manager.destroy_agent(self.agent_id)
        except Exception:
            logger.debug("destroy_agent ignored exception for key=%s", self.key, exc_info=True)
        self.manager.stop()


class GraphFlowAdapter(AgentAdapter):
    name = "graphflow"

    def __init__(self, *, api_key=None, base_url=None, model=None, **_options) -> None:
        self.api_key = api_key or settings.GRAPHFLOW_API_KEY
        self.base_url = base_url or settings.GRAPHFLOW_BASE_URL
        self.model = model or settings.GRAPHFLOW_MODEL
        self.content_mode = "snapshot" if settings.GRAPHFLOW_PROVIDER == "anthropic" else "delta"

    def complete(self, messages: list[dict], **options) -> LLMResponse:
        sdk = load_sdk()
        system_prompt, query = format_messages_for_query(messages)
        config = build_config(
            system_prompt=system_prompt,
            working_directory=options.get("working_directory", ""),
            provider_override=options.get("provider_override"),
        )
        with sdk.Engine(
            config,
            api_key=self.api_key,
            base_url=self.base_url,
            model=self.model,
        ) as engine:
            result = engine.query(query)
        return LLMResponse(
            content=result.final_answer,
            usage=TokenUsage(
                prompt_tokens=result.token_usage.prompt_tokens,
                completion_tokens=result.token_usage.completion_tokens,
                total_tokens=result.token_usage.total_tokens,
            ),
            model=self.model,
            success=result.success,
            error=result.error_message or None,
        )

    def create_session(
        self,
        key: str,
        *,
        system_prompt: str,
        working_directory: str = "",
        enable_permissions: Optional[bool] = None,
        options: Optional[dict] = None,
    ) -> GraphFlowSession:
        sdk = load_sdk()
        manager = sdk.Manager()
        provider_override = options or {}
        config = build_config(
            system_prompt=system_prompt,
            working_directory=working_directory,
            enable_permissions=enable_permissions,
            provider_override=provider_override,
        )
        try:
            agent_id = manager.create_agent(
                config,
                api_key=provider_override.get("api_key") or self.api_key,
                base_url=provider_override.get("base_url") or self.base_url,
                model=provider_override.get("model") or self.model,
            )
        except Exception:
            manager.stop()
            raise
        now = time.monotonic()
        return GraphFlowSession(
            key=key,
            manager=manager,
            agent_id=agent_id,
            created_at=now,
            last_used_at=now,
            stream_lock=threading.Lock(),
            content_mode=(
                "snapshot"
                if (provider_override.get("provider") or settings.GRAPHFLOW_PROVIDER) == "anthropic"
                else "delta"
            ),
        )
