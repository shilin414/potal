"""
LangChain ReAct Agent Example
=============================

Demonstrates the ReAct (Reasoning + Acting) pattern using LangChain:
1. Reasoning - LLM analyzes the question and decides which tool to use
2. Acting - Execute the chosen tool and get results
3. Observation - Feed results back to LLM for next step
4. Repeat until task complete

This mirrors the ReAct pattern used in the C++ Agent Engine
(engine/agent/react_executor.h).

Usage:
    # Set your API key first
    export DEEPSEEK_API_KEY="your-api-key"

    # Run with default question
    python react_agent.py

    # Run with custom question
    python react_agent.py --question "What is the weather in Beijing and Shanghai?"
"""

import argparse
import os
import sys
from typing import Annotated

from langchain_core.messages import HumanMessage, SystemMessage
from langchain_core.prompts import ChatPromptTemplate, MessagesPlaceholder
from langchain_core.tools import tool
from langchain_openai import ChatOpenAI

# ---------------------------------------------------------------------------
# 1. Define Tools
# ---------------------------------------------------------------------------

@tool
def get_weather(city: Annotated[str, "城市名称，如：北京、上海、深圳"]) -> str:
    """获取指定城市的天气信息。

    Args:
        city: 城市名称

    Returns:
        天气信息字符串
    """
    # 模拟天气数据（实际项目中应调用真实天气 API）
    weather_db = {
        "北京": "晴天，气温 28°C，空气质量良好，微风 2级",
        "上海": "多云转阴，气温 32°C，湿度 75%，东南风 3级",
        "深圳": "雷阵雨，气温 30°C，湿度 85%，南风 4级",
        "广州": "大雨，气温 29°C，湿度 90%，西南风 5级",
        "成都": "阴天，气温 25°C，湿度 60%，微风 1级",
        "杭州": "小雨，气温 27°C，湿度 80%，东风 2级",
    }
    return weather_db.get(city, f"暂无 {city} 的天气数据，请尝试其他城市")


@tool
def calculate(expression: Annotated[str, "数学表达式，如：'2 + 3 * 4'"]) -> str:
    """计算数学表达式。

    Args:
        expression: 数学表达式字符串

    Returns:
        计算结果字符串
    """
    try:
        # 只允许安全的数学运算
        allowed_chars = set("0123456789+-*/.() ")
        if not all(c in allowed_chars for c in expression):
            return "错误：表达式包含不允许的字符，仅支持数字和 + - * / ( )"
        result = eval(expression)  # noqa: S307
        return f"{expression} = {result}"
    except Exception as e:
        return f"计算错误：{e}"


@tool
def search_knowledge(
    query: Annotated[str, "搜索关键词或问题"],
) -> str:
    """在知识库中搜索相关信息。

    Args:
        query: 搜索关键词

    Returns:
        搜索结果字符串
    """
    # 模拟知识库搜索（实际项目中应连接向量数据库或搜索引擎）
    knowledge_db = {
        "Python": "Python 是一种广泛使用的高级编程语言，由 Guido van Rossum 于 1991 年发布。"
                  "它支持多种编程范式，包括面向对象、函数式和过程式编程。",
        "LangChain": "LangChain 是一个用于构建 LLM 应用的框架，提供了链式调用、"
                     "工具集成、记忆管理等核心能力。支持 ReAct、Plan-and-Execute 等多种 Agent 模式。",
        "ReAct": "ReAct (Reasoning + Acting) 是一种将推理和行动结合的范式。"
                 "Agent 在每一步先推理(Thought)，然后采取行动(Action)，"
                 "再观察结果(Observation)，循环往复直到完成任务。",
        "Agent": "Agent 是能够自主决策和执行任务的 AI 系统。"
                 "核心组件包括：LLM（大脑）、Tools（手脚）、Memory（记忆）、"
                 "Planning（规划）。LangChain 提供了多种 Agent 实现。",
        "vLLM": "vLLM 是一个高性能的 LLM 推理引擎，支持 PagedAttention 技术，"
                "可用于部署和加速大语言模型的推理服务。",
    }

    results = []
    for key, value in knowledge_db.items():
        if key.lower() in query.lower() or query.lower() in key.lower():
            results.append(f"【{key}】{value}")

    if results:
        return "\n".join(results)
    return f"未找到与 '{query}' 相关的信息，请尝试其他关键词"


# Collect all tools
ALL_TOOLS = [get_weather, calculate, search_knowledge]


# ---------------------------------------------------------------------------
# 2. Build ReAct Agent
# ---------------------------------------------------------------------------

def create_react_agent(api_key: str, base_url: str = None, model: str = None):
    """创建 LangChain ReAct Agent。

    Args:
        api_key: LLM API Key
        base_url: API 基础 URL（可选，默认使用 DeepSeek）
        model: 模型名称（可选）

    Returns:
        配置好的 Agent 对象
    """
    # 使用 DeepSeek 或其他 OpenAI 兼容的 API
    kwargs = {
        "api_key": api_key,
        "model": model or "deepseek-chat",
        "temperature": 0,
    }
    if base_url:
        kwargs["base_url"] = base_url

    llm = ChatOpenAI(**kwargs)

    # 将工具绑定到 LLM
    llm_with_tools = llm.bind_tools(ALL_TOOLS)

    # 构建 ReAct 提示词模板
    # 注意：工具列表由 bind_tools() 自动注入，无需在 prompt 中列出
    # ReAct 循环由 AgentExecutor + agent_scratchpad 自动管理
    system_prompt = (
        "你是一个智能助手，通过调用工具来回答用户的问题。\n"
        "请使用中文回答，必要时调用多个工具获取完整信息。"
    )

    prompt = ChatPromptTemplate.from_messages([
        ("system", system_prompt),
        MessagesPlaceholder("chat_history", optional=True),
        ("human", "{input}"),
        MessagesPlaceholder("agent_scratchpad"),
    ])

    # 使用 LangChain 内置的 create_tool_calling_agent
    from langchain.agents import create_tool_calling_agent, AgentExecutor

    agent = create_tool_calling_agent(llm_with_tools, ALL_TOOLS, prompt)

    # 创建 AgentExecutor，控制执行流程
    agent_executor = AgentExecutor(
        agent=agent,
        tools=ALL_TOOLS,
        verbose=True,           # 打印中间推理过程
        max_iterations=5,       # 最大推理轮次
        handle_parsing_errors=True,
    )

    return agent_executor


# ---------------------------------------------------------------------------
# 3. Run Agent
# ---------------------------------------------------------------------------

def run_interactive(agent_executor):
    """交互式运行 Agent。"""
    print("=" * 60)
    print("  LangChain ReAct Agent - 交互模式")
    print("  输入 'quit' 或 'exit' 退出")
    print("=" * 60)
    print()

    while True:
        try:
            user_input = input("🙋 用户: ").strip()
            if not user_input:
                continue
            if user_input.lower() in ("quit", "exit", "q"):
                print("再见！")
                break

            print()
            result = agent_executor.invoke({"input": user_input})
            print(f"\n🤖 助手: {result['output']}\n")

        except KeyboardInterrupt:
            print("\n\n再见！")
            break
        except Exception as e:
            print(f"\n❌ 错误: {e}\n")


def run_single(agent_executor, question: str):
    """单次运行 Agent。"""
    print(f"🙋 用户: {question}\n")
    result = agent_executor.invoke({"input": question})
    print(f"\n🤖 助手: {result['output']}")
    return result


# ---------------------------------------------------------------------------
# 4. Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="LangChain ReAct Agent 示例")
    parser.add_argument(
        "--question", "-q",
        type=str,
        default="北京和上海今天的天气怎么样？哪个城市更热？温度差多少？",
        help="要提问的问题",
    )
    parser.add_argument(
        "--interactive", "-i",
        action="store_true",
        help="进入交互模式",
    )
    parser.add_argument(
        "--api-key",
        type=str,
        default=os.environ.get("DEEPSEEK_API_KEY", ""),
        help="DeepSeek API Key (或设置 DEEPSEEK_API_KEY 环境变量)",
    )
    parser.add_argument(
        "--base-url",
        type=str,
        default=os.environ.get("DEEPSEEK_BASE_URL", "https://api.deepseek.com"),
        help="API Base URL",
    )
    parser.add_argument(
        "--model",
        type=str,
        default=os.environ.get("DEEPSEEK_MODEL", "deepseek-chat"),
        help="模型名称",
    )
    args = parser.parse_args()

    if not args.api_key:
        print("错误：请设置 DEEPSEEK_API_KEY 环境变量，或通过 --api-key 参数传入")
        print("  export DEEPSEEK_API_KEY='your-api-key'")
        sys.exit(1)

    agent_executor = create_react_agent(
        api_key=args.api_key,
        base_url=args.base_url,
        model=args.model,
    )

    if args.interactive:
        run_interactive(agent_executor)
    else:
        run_single(agent_executor, args.question)


if __name__ == "__main__":
    main()
