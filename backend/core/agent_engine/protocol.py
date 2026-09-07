"""Codex App Server-shaped protocol shared by every agent adapter."""
from __future__ import annotations

import json
import time
import uuid
from dataclasses import dataclass
from typing import Any, Iterable

from .adapters.base import AgentEvent, EventType, ProgressCategory


class ProtocolMethod:
    TURN_STARTED = "turn/started"
    TURN_COMPLETED = "turn/completed"
    ITEM_STARTED = "item/started"
    ITEM_COMPLETED = "item/completed"
    AGENT_MESSAGE_DELTA = "item/agentMessage/delta"
    REASONING_TEXT_DELTA = "item/reasoning/textDelta"
    SKILLS_CHANGED = "skills/changed"
    REQUEST_USER_INPUT = "tool/requestUserInput"
    COMMAND_APPROVAL = "item/commandExecution/requestApproval"


@dataclass(frozen=True)
class ProtocolEvent:
    """One SSE-safe Codex-style server notification."""

    sequence: int
    method: str
    params: dict[str, Any]
    emitted_at_ms: int

    def to_dict(self) -> dict[str, Any]:
        return {
            "sequence": self.sequence,
            "method": self.method,
            "params": self.params,
            "emittedAtMs": self.emitted_at_ms,
        }

    def to_sse(self) -> str:
        data = json.dumps(self.to_dict(), ensure_ascii=False)
        return f"id: {self.sequence}\nevent: {self.method}\ndata: {data}\n\n"


class ProtocolProjector:
    """Project adapter-neutral events onto the Codex thread/turn/item model."""

    def __init__(self, *, thread_id: str, turn_id: str) -> None:
        self.thread_id = str(thread_id)
        self.turn_id = str(turn_id)
        self._sequence = 0
        self._message_item_id = ""
        self._message_text = ""
        self._reasoning_item_id = ""
        self._reasoning_text = ""
        self._items: dict[str, dict[str, Any]] = {}

    def _emit(self, method: str, params: dict[str, Any]) -> ProtocolEvent:
        self._sequence += 1
        return ProtocolEvent(
            sequence=self._sequence,
            method=method,
            params=params,
            emitted_at_ms=int(time.time() * 1000),
        )

    def start_turn(self) -> ProtocolEvent:
        return self._emit(ProtocolMethod.TURN_STARTED, {
            "threadId": self.thread_id,
            "turn": {
                "id": self.turn_id,
                "status": "inProgress",
                "items": [],
            },
        })

    def project(self, event: AgentEvent) -> list[ProtocolEvent]:
        if event.type == EventType.PROGRESS:
            if event.progress_category == ProgressCategory.CONTENT:
                return self._project_message_delta(event)
            if event.progress_category == ProgressCategory.THINKING:
                return self._project_reasoning_delta(event)
            if event.progress_category == ProgressCategory.SKILL:
                return [self._emit(ProtocolMethod.SKILLS_CHANGED, {
                    "threadId": self.thread_id,
                    "turnId": self.turn_id,
                    "skills": event.extras.get("loaded_skills", []),
                    "missingSkills": event.extras.get("missing_skills", []),
                })]
            return []
        if event.type in (EventType.TOOL_START, EventType.TOOL_END):
            return self._project_tool(event)
        if event.type in (EventType.QUESTION, EventType.PERMISSION):
            return self._project_request(event)
        if event.type in (EventType.COMPLETED, EventType.FAILED, EventType.CANCELLED):
            return self._project_completion(event)
        return []

    def snapshot_items(self) -> list[dict[str, Any]]:
        """Return the latest item values in original insertion order."""
        return [dict(item) for item in self._items.values()]

    def _item_id(self, event: AgentEvent, prefix: str) -> str:
        return event.request_id or f"{prefix}_{uuid.uuid4().hex}"

    def _started(self, item: dict[str, Any]) -> ProtocolEvent:
        self._items[item["id"]] = dict(item)
        return self._emit(ProtocolMethod.ITEM_STARTED, {
            "threadId": self.thread_id,
            "turnId": self.turn_id,
            "item": item,
            "startedAtMs": int(time.time() * 1000),
        })

    def _completed(self, item: dict[str, Any]) -> ProtocolEvent:
        self._items[item["id"]] = dict(item)
        return self._emit(ProtocolMethod.ITEM_COMPLETED, {
            "threadId": self.thread_id,
            "turnId": self.turn_id,
            "item": item,
            "completedAtMs": int(time.time() * 1000),
        })

    def _project_message_delta(self, event: AgentEvent) -> list[ProtocolEvent]:
        emitted: list[ProtocolEvent] = []
        requested_id = event.request_id
        if not self._message_item_id or (
            requested_id and requested_id != self._message_item_id
        ):
            self._message_item_id = self._item_id(event, "msg")
            self._message_text = ""
            emitted.append(self._started({
                "id": self._message_item_id,
                "type": "agentMessage",
                "text": "",
            }))
        snapshot = event.extras.get("content_mode") == "snapshot"
        if (
            snapshot
            and self._message_text
            and not event.content.startswith(self._message_text)
        ):
            # A rewritten snapshot cannot be represented as a text delta.
            # Close the old item and start a new one to preserve ordering.
            emitted.append(self._completed(self._items[self._message_item_id]))
            self._message_item_id = f"msg_{uuid.uuid4().hex}"
            self._message_text = ""
            emitted.append(self._started({
                "id": self._message_item_id,
                "type": "agentMessage",
                "text": "",
            }))
        delta = (
            event.content[len(self._message_text):]
            if snapshot else event.content
        )
        self._message_text = (
            event.content if snapshot else self._message_text + event.content
        )
        self._items[self._message_item_id]["text"] = self._message_text
        if delta:
            emitted.append(self._emit(ProtocolMethod.AGENT_MESSAGE_DELTA, {
                "threadId": self.thread_id,
                "turnId": self.turn_id,
                "itemId": self._message_item_id,
                "delta": delta,
            }))
        return emitted

    def _project_reasoning_delta(self, event: AgentEvent) -> list[ProtocolEvent]:
        emitted: list[ProtocolEvent] = []
        if not self._reasoning_item_id:
            self._reasoning_item_id = self._item_id(event, "reasoning")
            self._reasoning_text = ""
            emitted.append(self._started({
                "id": self._reasoning_item_id,
                "type": "reasoning",
                "summary": [],
                "content": [],
            }))
        self._reasoning_text += event.content
        self._items[self._reasoning_item_id]["content"] = [self._reasoning_text]
        emitted.append(self._emit(ProtocolMethod.REASONING_TEXT_DELTA, {
            "threadId": self.thread_id,
            "turnId": self.turn_id,
            "itemId": self._reasoning_item_id,
            "delta": event.content,
            "contentIndex": 0,
        }))
        return emitted

    def _project_tool(self, event: AgentEvent) -> list[ProtocolEvent]:
        tool_call = event.tool_call or {}
        item_id = str(tool_call.get("id") or self._item_id(event, "tool"))
        item = self._tool_item(item_id, tool_call, completed=(
            event.type == EventType.TOOL_END
        ))
        if event.type == EventType.TOOL_START:
            # A later assistant message must become a separate item.
            self._message_item_id = ""
            self._message_text = ""
            return [self._started(item)]
        return [self._completed(item)]

    def _project_request(self, event: AgentEvent) -> list[ProtocolEvent]:
        method = (
            ProtocolMethod.COMMAND_APPROVAL
            if event.type == EventType.PERMISSION
            else ProtocolMethod.REQUEST_USER_INPUT
        )
        request_id = event.request_id or f"request_{uuid.uuid4().hex}"
        return [self._emit(method, {
            "requestId": request_id,
            "threadId": self.thread_id,
            "turnId": self.turn_id,
            "question": event.question or {},
        })]

    def _project_completion(self, event: AgentEvent) -> list[ProtocolEvent]:
        emitted: list[ProtocolEvent] = []
        if event.content and not self._message_text:
            emitted.extend(self._project_message_delta(event))
        if self._reasoning_item_id:
            emitted.append(self._completed(self._items[self._reasoning_item_id]))
            self._reasoning_item_id = ""
        if self._message_item_id:
            emitted.append(self._completed(self._items[self._message_item_id]))
            self._message_item_id = ""
        status = {
            EventType.COMPLETED: "completed",
            EventType.FAILED: "failed",
            EventType.CANCELLED: "interrupted",
        }[event.type]
        turn: dict[str, Any] = {
            "id": self.turn_id,
            "status": status,
            "items": self.snapshot_items(),
        }
        if event.usage is not None:
            turn["usage"] = event.usage
        if event.error_message:
            turn["error"] = {
                "code": event.error_code or "agent_error",
                "message": event.error_message,
            }
        emitted.append(self._emit(ProtocolMethod.TURN_COMPLETED, {
            "threadId": self.thread_id,
            "turn": turn,
        }))
        return emitted

    @staticmethod
    def _decode(value: Any) -> Any:
        if not isinstance(value, str):
            return value
        try:
            return json.loads(value)
        except (TypeError, ValueError):
            return value

    def _tool_item(
        self,
        item_id: str,
        tool_call: dict[str, Any],
        *,
        completed: bool,
    ) -> dict[str, Any]:
        name = str(tool_call.get("name") or "tool")
        arguments = self._decode(tool_call.get("input", {}))
        status = str(tool_call.get("status") or "")
        success = status.lower() in {"completed", "succeeded", "success"}
        if name == "command":
            command_data = arguments if isinstance(arguments, dict) else {}
            return {
                "id": item_id,
                "type": "commandExecution",
                "command": command_data.get("command", str(arguments)),
                "cwd": command_data.get("cwd", ""),
                "commandActions": [],
                "aggregatedOutput": tool_call.get("result", "") if completed else "",
                "status": "completed" if completed and success else (
                    "failed" if completed else "inProgress"
                ),
            }
        if name == "apply_patch":
            changes = arguments if isinstance(arguments, list) else []
            return {
                "id": item_id,
                "type": "fileChange",
                "changes": changes,
                "status": "completed" if completed and success else (
                    "failed" if completed else "inProgress"
                ),
            }
        if name == "web_search":
            query = arguments.get("query", "") if isinstance(arguments, dict) else arguments
            return {
                "id": item_id,
                "type": "webSearch",
                "query": str(query or ""),
                "results": self._decode(tool_call.get("result")) if completed else None,
            }
        return {
            "id": item_id,
            "type": "dynamicToolCall",
            "tool": name,
            "namespace": "graphflow",
            "arguments": arguments,
            "contentItems": [],
            "status": "completed" if completed and success else (
                "failed" if completed else "inProgress"
            ),
            "success": success if completed else None,
            "result": self._decode(tool_call.get("result")) if completed else None,
            "error": tool_call.get("error_message") or None,
        }


def encode_sse(events: Iterable[ProtocolEvent]) -> str:
    return "".join(event.to_sse() for event in events)
