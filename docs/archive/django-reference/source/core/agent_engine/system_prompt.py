"""System prompt builder.

Following claw_agent_engine's SystemPromptSections pattern — multi-layer
system prompt construction:
  Layer 1: Built-in default prompt (core agent instructions)
  Layer 2: Custom system prompt (user-provided)
  Layer 3: Tool definitions (auto-injected)
  Layer 4: Append system prompt (additional user instructions)
"""

from __future__ import annotations


# Default system prompt — inspired by claw_agent_engine's built-in defaults
DEFAULT_SYSTEM_PROMPT = """You are a helpful assistant that can use tools to help the user.

Core capabilities:
- You can read and write files to help with coding tasks
- You can generate images and videos for creative tasks
- You can search and analyze information from files

Guidelines:
- Be concise and direct in your responses
- Use tools when they can help accomplish the task
- Always verify file paths before reading or writing
- When generating media, provide clear, detailed prompts
- If a tool fails, explain the error and suggest alternatives"""

# Memory mechanics prompt — for session management
MEMORY_MECHANICS_PROMPT = """You are in a multi-turn conversation.
- You have access to conversation history
- Keep responses self-contained; don't rely on the user remembering previous details
- When switching topics, clearly indicate the context change"""


class SystemPromptBuilder:
    """Builds multi-layer system prompt following claw_agent_engine's pattern.

    Layers:
    1. Default/built-in instructions
    2. Custom system prompt (user-provided)
    3. Tool definitions (auto-generated from ToolManager)
    4. Append system prompt (additional instructions)
    """

    def __init__(
        self,
        default_prompt: str = DEFAULT_SYSTEM_PROMPT,
        custom_prompt: str = "",
        append_prompt: str = "",
        memory_prompt: str = MEMORY_MECHANICS_PROMPT,
    ) -> None:
        self._default_prompt = default_prompt
        self._custom_prompt = custom_prompt
        self._append_prompt = append_prompt
        self._memory_prompt = memory_prompt
        self._tool_definitions: str = ""

    def set_default_prompt(self, prompt: str) -> None:
        """Set the default system prompt (Layer 1)."""
        self._default_prompt = prompt

    def set_custom_prompt(self, prompt: str) -> None:
        """Set the custom system prompt (Layer 2)."""
        self._custom_prompt = prompt

    def set_append_prompt(self, prompt: str) -> None:
        """Set the append system prompt (Layer 4)."""
        self._append_prompt = prompt

    def set_tool_definitions(self, tools: list[dict]) -> None:
        """Set tool definitions from ToolManager.

        Args:
            tools: List of tool definitions in API format.
        """
        if tools:
            tool_section = "# Available Tools\n"
            for tool in tools:
                tool_section += f"\n## {tool['name']}\n"
                tool_section += f"{tool['description']}\n"
                if tool.get('input_schema'):
                    params = tool['input_schema'].get('properties', {})
                    required = tool['input_schema'].get('required', [])
                    tool_section += "Parameters:\n"
                    for name, schema in params.items():
                        req = " (required)" if name in required else ""
                        desc = schema.get('description', '')
                        tool_section += f"  - {name}{req}: {desc}\n"
            self._tool_definitions = tool_section
        else:
            self._tool_definitions = ""

    def build(self) -> str:
        """Build the complete system prompt.

        Returns:
            Combined system prompt string.
        """
        parts = []

        # Layer 1: Default prompt
        if self._default_prompt:
            parts.append(self._default_prompt)

        # Layer 2: Custom prompt
        if self._custom_prompt:
            parts.append(f"# Custom Instructions\n{self._custom_prompt}")

        # Layer 2b: Tool definitions
        if self._tool_definitions:
            parts.append(self._tool_definitions)

        # Layer 3: Memory mechanics
        if self._memory_prompt:
            parts.append(self._memory_prompt)

        # Layer 4: Append prompt
        if self._append_prompt:
            parts.append(f"# Additional Instructions\n{self._append_prompt}")

        return "\n\n".join(parts)

    def get_prompt(self) -> str:
        """Alias for build()."""
        return self.build()

    def reset(self) -> None:
        """Reset to defaults."""
        self._custom_prompt = ""
        self._append_prompt = ""
        self._tool_definitions = ""