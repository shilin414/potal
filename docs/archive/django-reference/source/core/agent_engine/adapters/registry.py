"""Registration and construction of agent adapters."""
from __future__ import annotations

import threading
from collections.abc import Callable

from django.conf import settings

from .base import AgentAdapter

AdapterFactory = Callable[..., AgentAdapter]


class AdapterRegistry:
    """Thread-safe registry that allows applications to add agent backends."""

    def __init__(self) -> None:
        self._factories: dict[str, AdapterFactory] = {}
        self._lock = threading.RLock()

    def register(self, name: str, factory: AdapterFactory, *, replace: bool = False) -> None:
        normalized = name.strip().lower()
        if not normalized:
            raise ValueError("Agent adapter name cannot be empty")
        with self._lock:
            if normalized in self._factories and not replace:
                raise ValueError(f"Agent adapter already registered: {normalized}")
            self._factories[normalized] = factory

    def create(self, name: str = "", **options) -> AgentAdapter:
        normalized = (name or settings.AGENT_ENGINE_ADAPTER).strip().lower()
        with self._lock:
            factory = self._factories.get(normalized)
            available = tuple(sorted(self._factories))
        if factory is None:
            choices = ", ".join(available) or "none"
            raise ValueError(
                f"Unknown agent adapter {normalized!r}. Registered adapters: {choices}"
            )
        return factory(**options)

    def names(self) -> tuple[str, ...]:
        with self._lock:
            return tuple(sorted(self._factories))


adapter_registry = AdapterRegistry()


def register_agent_adapter(
    name: str, factory: AdapterFactory, *, replace: bool = False
) -> None:
    adapter_registry.register(name, factory, replace=replace)


def get_agent_adapter(name: str = "", **options) -> AgentAdapter:
    return adapter_registry.create(name, **options)
