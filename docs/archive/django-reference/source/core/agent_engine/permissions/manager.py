"""Permission manager for tool execution.

Following claw_agent_engine's PermissionManager pattern:
- PermissionLevel: Enum for permission levels (safe, warn, confirm, block)
- PermissionRule: Rule mapping tool name to permission level
- PermissionDecision: Decision result (allow/deny/ask)
- PermissionManager: Manages rules and checks permissions
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum


class PermissionLevel(str, Enum):
    """Permission levels for tool execution.

    Following claw_agent_engine's PermissionLevel pattern:
    - SAFE: Always allow without asking
    - WARN: Allow but notify the user
    - CONFIRM: Ask user for confirmation before execution
    - BLOCK: Always deny execution
    """

    SAFE = "safe"
    WARN = "warn"
    CONFIRM = "confirm"
    BLOCK = "block"


class PermissionBehavior(str, Enum):
    """Behavior returned by permission check."""

    ALLOW = "allow"
    DENY = "deny"
    ASK = "ask"


@dataclass
class PermissionRule:
    """Rule defining permission level for a tool."""

    tool_name: str
    level: PermissionLevel = PermissionLevel.SAFE
    reason: str = ""


@dataclass
class PermissionDecision:
    """Decision from a permission check."""

    behavior: PermissionBehavior
    level: PermissionLevel = PermissionLevel.SAFE
    reason: str = ""

    @classmethod
    def allow(cls, level: PermissionLevel = PermissionLevel.SAFE, reason: str = "") -> PermissionDecision:
        return cls(behavior=PermissionBehavior.ALLOW, level=level, reason=reason)

    @classmethod
    def deny(cls, reason: str = "") -> PermissionDecision:
        return cls(behavior=PermissionBehavior.DENY, level=PermissionLevel.BLOCK, reason=reason)

    @classmethod
    def ask(cls, reason: str = "") -> PermissionDecision:
        return cls(behavior=PermissionBehavior.ASK, level=PermissionLevel.CONFIRM, reason=reason)


class PermissionManager:
    """Manages permission rules for tool execution.

    Following claw_agent_engine's PermissionManager pattern:
    - set_rule: Set permission rule for a tool
    - check: Check permission for a tool
    - set_default_level: Set default permission level
    - set_bypass: Enable/disable permission bypass
    """

    # Default rules following claw_agent_engine's defaults
    DEFAULT_RULES: dict[str, PermissionLevel] = {
        # Safe - read-only operations
        "read_file": PermissionLevel.SAFE,
        "search_files": PermissionLevel.SAFE,
        "list_files": PermissionLevel.SAFE,
        # Warn - operations that modify files
        "write_file": PermissionLevel.WARN,
        "edit_file": PermissionLevel.WARN,
        # Confirm - operations that execute commands
        "bash": PermissionLevel.CONFIRM,
        "execute": PermissionLevel.CONFIRM,
        # Block - dangerous operations
        "rm": PermissionLevel.BLOCK,
        "delete_file": PermissionLevel.BLOCK,
    }

    def __init__(
        self,
        default_level: PermissionLevel = PermissionLevel.SAFE,
        enable_permissions: bool = True,
    ) -> None:
        self._default_level = default_level
        self._enabled = enable_permissions
        self._bypass = False
        self._rules: dict[str, PermissionRule] = {}

        # Initialize with default rules
        for tool_name, level in self.DEFAULT_RULES.items():
            self._rules[tool_name] = PermissionRule(tool_name=tool_name, level=level)

    def set_rule(self, tool_name: str, level: PermissionLevel, reason: str = "") -> None:
        """Set permission rule for a tool.

        Args:
            tool_name: Tool name.
            level: Permission level.
            reason: Optional reason for the rule.
        """
        self._rules[tool_name] = PermissionRule(
            tool_name=tool_name,
            level=level,
            reason=reason,
        )

    def get_rule(self, tool_name: str) -> PermissionRule:
        """Get permission rule for a tool.

        Args:
            tool_name: Tool name.

        Returns:
            PermissionRule for the tool.
        """
        return self._rules.get(
            tool_name,
            PermissionRule(tool_name=tool_name, level=self._default_level),
        )

    def check(self, tool_name: str, tool_input: str = "") -> PermissionDecision:
        """Check permission for a tool execution.

        Args:
            tool_name: Tool name.
            tool_input: Tool input (for context).

        Returns:
            PermissionDecision with the decision.
        """
        if not self._enabled or self._bypass:
            return PermissionDecision.allow(reason="Permissions bypassed")

        rule = self.get_rule(tool_name)

        if rule.level == PermissionLevel.SAFE:
            return PermissionDecision.allow(level=rule.level, reason=rule.reason or "Safe operation")

        if rule.level == PermissionLevel.WARN:
            return PermissionDecision.allow(
                level=rule.level,
                reason=rule.reason or "Warning: This modifies files",
            )

        if rule.level == PermissionLevel.CONFIRM:
            return PermissionDecision.ask(reason=rule.reason or "Requires confirmation")

        if rule.level == PermissionLevel.BLOCK:
            return PermissionDecision.deny(reason=rule.reason or "Blocked operation")

        # Default: allow with safe level
        return PermissionDecision.allow()

    def set_default_level(self, level: PermissionLevel) -> None:
        """Set default permission level for new tools.

        Args:
            level: Default permission level.
        """
        self._default_level = level

    def set_bypass(self, bypass: bool) -> None:
        """Enable/disable permission bypass (for testing).

        Args:
            bypass: True to bypass all permission checks.
        """
        self._bypass = bypass

    def is_enabled(self) -> bool:
        """Check if permissions are enabled.

        Returns:
            True if permissions are enabled.
        """
        return self._enabled

    def is_bypassed(self) -> bool:
        """Check if permissions are bypassed.

        Returns:
            True if permissions are bypassed.
        """
        return self._bypass