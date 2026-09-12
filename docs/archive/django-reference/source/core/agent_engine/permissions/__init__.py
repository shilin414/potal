"""Agent Engine Permissions Framework.

Following claw_agent_engine's PermissionManager pattern:
- PermissionLevel: Enum for permission levels
- PermissionDecision: Result of a permission check
- PermissionManager: Manages permission rules and checks
"""

from .manager import PermissionDecision, PermissionLevel, PermissionManager, PermissionRule

__all__ = [
    "PermissionLevel",
    "PermissionDecision",
    "PermissionRule",
    "PermissionManager",
]