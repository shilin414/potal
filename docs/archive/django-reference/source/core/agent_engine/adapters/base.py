"""Stable contracts shared by all agent runtime implementations."""
from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import Any, Optional

from ..models import LLMResponse


class EventType:
    PROGRESS = "progress"
    QUESTION = "question"
    PERMISSION = "permission"
    TOOL_START = "tool_start"
    TOOL_END = "tool_end"
    COMPLETED = "completed"
    FAILED = "failed"
    CANCELLED = "cancelled"


class ProgressCategory:
    CONTENT = "content"
    THINKING = "thinking"
    SKILL = "skill"


@dataclass(frozen=True)
class OperationStatus:
    """Backend-neutral result returned by resume and cancel operations."""

    value: str


@dataclass
class AgentEvent:
    """Backend-neutral event consumed by the Django SSE endpoint."""

    type: str
    agent_id: str
    content: str = ""
    progress_category: str = ""
    query_id: str = ""
    request_id: str = ""
    question: Optional[dict] = None
    tool_call: Optional[dict] = None
    usage: Optional[dict] = None
    error_code: str = ""
    error_message: str = ""
    extras: dict[str, Any] = field(default_factory=dict)

    def to_payload(self) -> dict:
        payload = {
            "type": self.type,
            "agent_id": self.agent_id,
            "content": self.content,
            "progress_category": self.progress_category,
            "query_id": self.query_id,
            "request_id": self.request_id,
        }
        if self.type == EventType.PROGRESS and self.progress_category == ProgressCategory.CONTENT:
            payload.update(is_assistant_content=True, content_mode="delta")
        if self.question is not None:
            payload["question"] = self.question
        if self.tool_call is not None:
            payload["tool_call"] = self.tool_call
        if self.usage is not None:
            payload["usage"] = self.usage
        if self.error_code:
            payload["error_code"] = self.error_code
        if self.error_message:
            payload["error_message"] = self.error_message
        payload.update(self.extras)
        return payload


class AgentAdapter(ABC):
    """Adapter implemented by every synchronous and streaming agent backend."""

    name: str
    content_mode = "delta"

    @abstractmethod
    def complete(self, messages: list[dict], **options) -> LLMResponse:
        """Run one completion and return the normalized result."""

    @abstractmethod
    def create_session(
        self,
        key: str,
        *,
        system_prompt: str,
        working_directory: str = "",
        enable_permissions: Optional[bool] = None,
        options: Optional[dict] = None,
    ):
        """Create a long-lived session exposing the common session methods."""
