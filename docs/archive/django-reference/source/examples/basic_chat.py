"""Example 1: Basic Chat with Conversation History.

Demonstrates:
- Creating an AgentEngine with configuration
- Using the simple chat() API (backward compatible)
- Conversation history management
- Token usage tracking
"""

import sys
import os

# Add parent directory to path for imports
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from core.agent_engine import AgentEngine, EngineConfig


def main() -> None:
    # Create engine configuration
    # In production, load from environment or config file
    config = EngineConfig(
        api_key=os.environ.get("LLM_API_KEY", "your-api-key-here"),
        model=os.environ.get("LLM_MODEL", "deepseek-chat"),
        base_url=os.environ.get("LLM_BASE_URL", "https://api.deepseek.com/v1"),
        temperature=0.7,
        max_tokens=2048,
    )

    # Create engine
    engine = AgentEngine(config)

    # Set system prompt (optional)
    engine.set_system_prompt(
        "You are a helpful assistant. Be concise and direct in your responses."
    )

    print("=" * 60)
    print("Basic Chat Example")
    print("=" * 60)
    print("Type 'quit' or 'exit' to end the conversation.")
    print("Type 'history' to see conversation history.")
    print("Type 'usage' to see token usage.")
    print("=" * 60)

    while True:
        user_input = input("\nYou: ").strip()
        if not user_input:
            continue
        if user_input.lower() in ("quit", "exit", "q"):
            print("\nGoodbye!")
            break
        if user_input.lower() == "history":
            messages = engine.get_messages()
            print(f"\nConversation history ({len(messages)} messages):")
            for i, msg in enumerate(messages):
                print(f"  [{i}] {msg['role']}: {msg['content'][:80]}...")
            continue
        if user_input.lower() == "usage":
            usage = engine.usage
            print(f"\nToken usage:")
            print(f"  Prompt tokens: {usage.prompt_tokens}")
            print(f"  Completion tokens: {usage.completion_tokens}")
            print(f"  Total tokens: {usage.total_tokens}")
            continue

        # Simple chat (no tool calling)
        print("\nAssistant: ", end="", flush=True)
        response = engine.chat(user_input)

        if response.success:
            print(response.content)
        else:
            print(f"\nError: {response.error}")


if __name__ == "__main__":
    main()