"""Example 3: Creating and Registering Custom Tools.

Demonstrates:
- Creating custom tools by extending BaseTool
- Defining tool parameters with proper schema
- Registering tools with the ToolManager
- Using custom tools in agent queries

Following claw_agent_engine's Tool pattern:
- BaseTool is the abstract base class
- Subclasses must implement: name, description, parameters, execute
"""

import sys
import os
import datetime

# Add parent directory to path for imports
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from pydantic import BaseModel

from core.agent_engine import (
    AgentEngine,
    EngineConfig,
    BaseTool,
    ToolParameter,
    ToolResult,
)


# ─── Custom Tool: Get Current Time ───

class GetCurrentTimeTool(BaseTool):
    """A tool that returns the current date and time."""

    @property
    def name(self) -> str:
        return "get_current_time"

    @property
    def description(self) -> str:
        return "Get the current date and time in a specified format."

    @property
    def parameters(self) -> list[ToolParameter]:
        return [
            ToolParameter(
                name="format",
                type="string",
                description="Date/time format (e.g., '%Y-%m-%d %H:%M:%S')",
                required=False,
                default="%Y-%m-%d %H:%M:%S",
            ),
            ToolParameter(
                name="timezone",
                type="string",
                description="Timezone name (e.g., 'UTC', 'Asia/Shanghai')",
                required=False,
                default="local",
            ),
        ]

    def execute(self, format: str = "%Y-%m-%d %H:%M:%S", **kwargs) -> ToolResult:
        try:
            now = datetime.datetime.now()
            formatted = now.strftime(format)
            return ToolResult.success(
                f"Current time: {formatted}",
                structured={"datetime": formatted, "format": format},
            )
        except Exception as exc:
            return ToolResult.error(f"Failed to get current time: {exc}")


# ─── Custom Tool: Calculator ───

class CalculatorTool(BaseTool):
    """A simple calculator tool for arithmetic operations."""

    @property
    def name(self) -> str:
        return "calculator"

    @property
    def description(self) -> str:
        return "Perform arithmetic calculations (add, subtract, multiply, divide)."

    @property
    def parameters(self) -> list[ToolParameter]:
        return [
            ToolParameter(
                name="expression",
                type="string",
                description="Math expression to evaluate (e.g., '2 + 3 * 4')",
                required=True,
            ),
        ]

    def execute(self, expression: str, **kwargs) -> ToolResult:
        try:
            # Safe evaluation of math expressions
            allowed = set("0123456789+-*/.() ")
            if not all(c in allowed for c in expression):
                return ToolResult.error("Invalid characters in expression")
            result = eval(expression, {"__builtins__": {}}, {})
            return ToolResult.success(
                f"{expression} = {result}",
                structured={"expression": expression, "result": result},
            )
        except Exception as exc:
            return ToolResult.error(f"Calculation error: {exc}")


# ─── Custom Tool: Word Counter ───

class WordCounterTool(BaseTool):
    """A tool that counts words in text."""

    @property
    def name(self) -> str:
        return "word_counter"

    @property
    def description(self) -> str:
        return "Count words, characters, and lines in a text."

    @property
    def parameters(self) -> list[ToolParameter]:
        return [
            ToolParameter(
                name="text",
                type="string",
                description="Text to analyze",
                required=True,
            ),
        ]

    def execute(self, text: str, **kwargs) -> ToolResult:
        try:
            words = len(text.split())
            chars = len(text)
            lines = len(text.splitlines())
            structured = {
                "words": words,
                "characters": chars,
                "lines": lines,
            }
            return ToolResult.success(
                f"Words: {words}, Characters: {chars}, Lines: {lines}",
                structured=structured,
            )
        except Exception as exc:
            return ToolResult.error(f"Word count error: {exc}")


def main() -> None:
    # Create engine
    config = EngineConfig(
        api_key=os.environ.get("LLM_API_KEY", "your-api-key-here"),
        model=os.environ.get("LLM_MODEL", "deepseek-chat"),
        base_url=os.environ.get("LLM_BASE_URL", "https://api.deepseek.com/v1"),
        max_turns=5,
    )

    engine = AgentEngine(config)

    # Register custom tools
    print("=" * 60)
    print("Custom Tool Example")
    print("=" * 60)

    engine.register_tool(GetCurrentTimeTool())
    engine.register_tool(CalculatorTool())
    engine.register_tool(WordCounterTool())

    # Show registered tools
    tools = engine.list_tools()
    print(f"\nRegistered tools ({len(tools)}):")
    for tool in tools:
        print(f"  - {tool['name']}: {tool['description']}")

    # Test custom tools via query
    print("\n" + "=" * 60)
    print("Testing custom tools via agent query")
    print("=" * 60)

    def on_message(msg: dict) -> None:
        msg_type = msg.get("type", "")
        if msg_type == "tool_use":
            print(f"  [Using: {msg['name']}]")
        elif msg_type == "tool_result":
            print(f"  [Result: {msg['content'][:100]}]")
        elif msg_type == "assistant_message":
            print(f"\n  Answer: {msg['content'][:100]}...")

    queries = [
        "What is the current time?",
        "Calculate 123 * 456 + 789",
        "Count the words in: 'The quick brown fox jumps over the lazy dog'",
    ]

    for query in queries:
        print(f"\nQuery: {query}")
        print("-" * 40)
        try:
            result = engine.query(query, on_message=on_message)
            if result.success:
                print(f"\n  Final: {result.content[:200]}")
        except Exception as e:
            print(f"\n  Exception: {e}")


if __name__ == "__main__":
    main()