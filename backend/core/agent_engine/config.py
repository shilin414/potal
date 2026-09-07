"""Engine configuration.

Following claw_agent_engine's Config pattern — a single dataclass that holds
all engine settings (LLM, tools, permissions, skills, session).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from .permissions.manager import PermissionLevel


@dataclass
class EngineConfig:
    """Engine configuration following claw_agent_engine's Config pattern.

    Attributes:
        api_key: LLM API key.
        model: LLM model name.
        base_url: LLM API base URL.
        temperature: Sampling temperature.
        max_tokens: Maximum tokens per response.
        max_retries: Maximum retry attempts on failure.
        timeout: HTTP timeout in seconds.
        max_turns: Maximum query turns (tool calling loop).
        max_history_rounds: Maximum history rounds before compression.
        max_context_messages: Maximum messages to keep in context.
        enable_permissions: Enable permission checking.
        default_permission_level: Default permission level for new tools.
        permission_bypass: Bypass all permission checks (testing).
        system_prompt: Custom system prompt.
        append_system_prompt: Additional system prompt appended at end.
        working_directory: Working directory for file tools.
        enable_skills: Enable skill loading.
        skills_directory: Skills directory path.
        enable_session_persistence: Enable session transcript recording.
        session_storage_path: Path for session files.
    """

    # LLM settings
    api_key: str = ""
    model: str = "deepseek-chat"
    base_url: str = "https://api.deepseek.com/v1"
    temperature: float = 0.7
    max_tokens: int = 4096
    max_retries: int = 2
    timeout: int = 60

    # Query engine settings
    max_turns: int = 25
    max_history_rounds: int = 50
    max_context_messages: int = 50

    # Permission settings
    enable_permissions: bool = True
    default_permission_level: PermissionLevel = PermissionLevel.SAFE
    permission_bypass: bool = False

    # System prompt
    system_prompt: str = ""
    append_system_prompt: str = ""

    # Working directory
    working_directory: str = "."

    # Skills settings
    enable_skills: bool = True
    skills_directory: str = ""

    # Session persistence
    enable_session_persistence: bool = False
    session_storage_path: str = ""

    # Auto-compaction
    enable_auto_compact: bool = True
    auto_compact_trigger_rounds: int = 50
    compact_keep_recent: int = 20
    compact_keep_initial: int = 4

    def to_dict(self) -> dict[str, Any]:
        """Convert config to dictionary."""
        return {
            "api_key": self.api_key,
            "model": self.model,
            "base_url": self.base_url,
            "temperature": self.temperature,
            "max_tokens": self.max_tokens,
            "max_retries": self.max_retries,
            "timeout": self.timeout,
            "max_turns": self.max_turns,
            "max_history_rounds": self.max_history_rounds,
            "max_context_messages": self.max_context_messages,
            "enable_permissions": self.enable_permissions,
            "default_permission_level": self.default_permission_level.value,
            "permission_bypass": self.permission_bypass,
            "system_prompt": self.system_prompt,
            "append_system_prompt": self.append_system_prompt,
            "working_directory": self.working_directory,
            "enable_skills": self.enable_skills,
            "skills_directory": self.skills_directory,
            "enable_session_persistence": self.enable_session_persistence,
            "session_storage_path": self.session_storage_path,
            "enable_auto_compact": self.enable_auto_compact,
            "auto_compact_trigger_rounds": self.auto_compact_trigger_rounds,
            "compact_keep_recent": self.compact_keep_recent,
            "compact_keep_initial": self.compact_keep_initial,
        }