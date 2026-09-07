"""Pluggable agent runtime for the studio backend."""
from __future__ import annotations

import logging

# Package logger. Submodule loggers (e.g. `core.agent_engine.runtime`) inherit
# this name space, so configuring Django's root logger propagates here too.
# NullHandler avoids "No handlers could be found" warnings when Django's LOGGING
# is not yet configured (e.g. during standalone imports or tests).
logger = logging.getLogger(__name__)
logger.addHandler(logging.NullHandler())
logger.info("agent_engine package initialized")

from .engine import AgentEngine
from .adapters import (
    AgentAdapter,
    AgentEvent,
    EventType,
    ProgressCategory,
    adapter_registry,
    get_agent_adapter,
    register_agent_adapter,
)
from .jiekou_ai_service import JieKouAIService
from .models import (
    AsyncTask,
    EngineConfig,
    ImageConfig,
    ImageResponse,
    JieKouConfig,
    VideoConfig,
    VideoTask,
)
from .runtime import event_payload, session_registry

__all__ = [
    "AgentEngine",
    "AgentAdapter",
    "AgentEvent",
    "AsyncTask",
    "EngineConfig",
    "EventType",
    "ImageConfig",
    "ImageResponse",
    "JieKouAIService",
    "JieKouConfig",
    "ProgressCategory",
    "VideoConfig",
    "VideoTask",
    "adapter_registry",
    "event_payload",
    "get_agent_adapter",
    "register_agent_adapter",
    "session_registry",
]
