"""Example 2: Tool Usage - File Operations with AgentEngine.

Demonstrates:
- Creating an AgentEngine with tools (file read/write/search)
- Listing registered tools
- Using the query() API for tool-enabled agent behavior
- Streaming progress callbacks

Following claw_agent_engine's tool usage pattern:
- The agent can automatically decide to use tools based on the user's request
- Tools are defined with proper schemas for the LLM to understand
- The agent executes tools and uses the results in subsequent responses
"""

import sys
import os

# Add parent directory to path for imports
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from core.agent_engine import (
    AgentEngine,
    EngineConfig,
    ToolManager,
    FileReadTool,
    FileWriteTool,
    FileSearchTool,
    PermissionLevel,
)


def demo_tool_info(engine: AgentEngine) -> None:
    """Display information about registered tools."""
    tools = engine.list_tools()
    print(f"\nRegistered tools ({len(tools)}):")
    print("-" * 40)
    for tool in tools:
        print(f"  Name: {tool['name']}")
        print(f"  Description: {tool['description']}")
        params = tool.get("parameters", {})
        if params.get("properties"):
            print(f"  Parameters: {list(params['properties'].keys())}")
        print()


def demo_permission_control(engine: AgentEngine) -> None:
    """Demonstrate permission control."""
    print("\nPermission control demo:")
    print("-" * 40)

    # Check default permissions
    for tool_name in ["read_file", "write_file", "search_files"]:
        rule = engine.permission_manager.get_rule(tool_name)
        print(f"  {tool_name}: {rule.level}")

    # Change permission
    print("\n  Changing write_file permission to CONFIRM...")
    engine.set_tool_permission("write_file", PermissionLevel.CONFIRM, "Requires user confirmation")
    rule = engine.permission_manager.get_rule("write_file")
    print(f"  write_file: {rule.level} (reason: {rule.reason})")


def main() -> None:
    # Create engine
    config = EngineConfig(
        api_key=os.environ.get("LLM_API_KEY", "your-api-key-here"),
        model=os.environ.get("LLM_MODEL", "deepseek-chat"),
        base_url=os.environ.get("LLM_BASE_URL", "https://api.deepseek.com/v1"),
        max_turns=10,  # Limit tool calling turns
    )

    engine = AgentEngine(config)

    # The engine already has file tools registered by default
    # But let's verify and show them
    print("=" * 60)
    print("Tool Usage Example")
    print("=" * 60)

    # Show registered tools
    demo_tool_info(engine)

    # Show permissions
    demo_permission_control(engine)

    # Example: Using query with streaming callback
    print("\n" + "=" * 60)
    print("Query with streaming callback")
    print("=" * 60)

    def on_message(msg: dict) -> None:
        """Callback for streaming progress updates."""
        msg_type = msg.get("type", "")
        if msg_type == "turn":
            print(f"\n  [Turn {msg['turn']}/{msg['max_turns']}]")
        elif msg_type == "tool_use":
            print(f"  [Using tool: {msg['name']}]")
        elif msg_type == "tool_result":
            status = "OK" if msg["success"] else "FAILED"
            print(f"  [{msg['name']}: {status}]")
        elif msg_type == "assistant_message":
            print(f"\n  Response: {msg['content'][:100]}...")

    # Query the agent
    query = (
        "Create a file called 'hello.txt' with the content 'Hello from AgentEngine!', "
        "then read it back and tell me what it contains."
    )

    print(f"\nQuery: {query}")
    print("-" * 40)

    try:
        result = engine.query(query, on_message=on_message)
        print("\n" + "=" * 60)
        print("Final Result:")
        print("=" * 60)
        if result.success:
            print(result.content)
        else:
            print(f"Error: {result.error}")
        print(f"\nTool calls made: {result.tool_calls}")
    except Exception as e:
        print(f"\nException: {e}")


if __name__ == "__main__":
    main()