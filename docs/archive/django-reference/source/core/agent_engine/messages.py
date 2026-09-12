"""Helpers for mapping OpenAI-shaped chat messages to agent turns."""
from __future__ import annotations

from collections.abc import Iterable


def format_messages_for_query(messages: Iterable[dict]) -> tuple[str, str]:
    """Return the combined system context and latest conversational message."""
    values = list(messages)
    system = "\n\n".join(
        str(item.get("content", "")) for item in values if item.get("role") == "system"
    )
    conversational = [item for item in values if item.get("role") != "system"]
    if not conversational:
        return system, ""
    latest = str(conversational[-1].get("content", ""))
    earlier = conversational[:-1]
    if earlier:
        transcript = "\n".join(
            f"{str(item.get('role', 'user')).upper()}: {item.get('content', '')}"
            for item in earlier
        )
        system = f"{system}\n\nConversation transcript:\n{transcript}".strip()
    return system, latest
