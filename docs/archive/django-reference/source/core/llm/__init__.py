"""LLM module — Django-side glue (engine factory + prompt assembly)."""
from .factory import build_agent_engine
from .prompt_manager import PromptManager

__all__ = ["build_agent_engine", "PromptManager"]
