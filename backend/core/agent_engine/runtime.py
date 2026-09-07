"""Django-facing registry shared by every configured agent adapter."""
from __future__ import annotations

import atexit
import threading
import time
from typing import Dict, Optional

from .adapters import AgentEvent, get_agent_adapter
from .adapters.graphflow import (
    GraphFlowSession as AgentSession,
    build_config,
    normalize_graphflow_event,
)
from .messages import format_messages_for_query


class AgentSessionRegistry:
    """Process-local conversation registry independent of agent implementation."""

    def __init__(self) -> None:
        self._sessions: Dict[str, object] = {}
        self._lock = threading.RLock()

    def get_or_create(
        self,
        key: str,
        *,
        system_prompt: str,
        working_directory: str = "",
        enable_permissions: Optional[bool] = None,
        provider_override: Optional[dict] = None,
        adapter_name: str = "",
        adapter_options: Optional[dict] = None,
    ):
        adapter = get_agent_adapter(adapter_name)
        options = dict(adapter_options or {})
        if adapter.name == "graphflow":
            options.update(provider_override or {})
        configuration_key = (
            adapter.name,
            system_prompt,
            working_directory,
            enable_permissions,
            tuple(sorted((option, repr(value)) for option, value in options.items())),
        )
        with self._lock:
            existing = self._sessions.get(key)
            if (
                existing is not None
                and getattr(existing, "configuration_key", None) == configuration_key
            ):
                existing.last_used_at = time.monotonic()
                return existing
            if existing is not None:
                self._sessions.pop(key, None)
                existing.close()

            session = adapter.create_session(
                key,
                system_prompt=system_prompt,
                working_directory=working_directory,
                enable_permissions=enable_permissions,
                options=options,
            )
            session.configuration_key = configuration_key
            session.adapter_name = adapter.name
            session.content_mode = adapter.content_mode
            self._sessions[key] = session
            return session

    def get(self, key: str):
        with self._lock:
            return self._sessions.get(key)

    def remove(self, key: str) -> None:
        with self._lock:
            session = self._sessions.pop(key, None)
        if session is not None:
            session.close()

    def close_all(self) -> None:
        with self._lock:
            sessions = list(self._sessions.values())
            self._sessions.clear()
        for session in sessions:
            session.close()


session_registry = AgentSessionRegistry()
atexit.register(session_registry.close_all)


def normalize_event(event) -> AgentEvent:
    """Normalize an adapter event, including legacy native GraphFlow events."""
    if isinstance(event, AgentEvent):
        return event
    return normalize_graphflow_event(event)


def event_payload(event) -> dict:
    """Return a JSON-safe payload for normalized or legacy GraphFlow events."""
    return normalize_event(event).to_payload()


__all__ = [
    "AgentSession",
    "AgentSessionRegistry",
    "build_config",
    "event_payload",
    "format_messages_for_query",
    "normalize_event",
    "session_registry",
]
