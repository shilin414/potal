# LangChain ReAct Agent 示例

使用 LangChain 框架实现 ReAct (Reasoning + Acting) 模式的完整示例。

## ReAct 模式说明

ReAct 是一种将 **推理 (Reasoning)** 和 **行动 (Acting)** 结合的 Agent 模式：

```
用户提问
    │
    ▼
┌─────────────────────────────────┐
│  Thought (推理)                  │  ← LLM 分析问题，决定下一步
│  "我需要查询北京和上海的天气"      │
├─────────────────────────────────┤
│  Action (行动)                   │  ← 调用工具
│  get_weather("北京")             │
├─────────────────────────────────┤
│  Observation (观察)              │  ← 获取工具结果
│  "北京：晴天，28°C"              │
├─────────────────────────────────┤
│  重复 Thought → Action → Obs    │  ← 直到收集到足够信息
├─────────────────────────────────┤
│  Final Answer (最终回答)         │  ← 综合所有信息给出答案
└─────────────────────────────────┘
```

这与项目中 C++ Agent Engine (`engine/agent/react_executor.h`) 的设计一致。

## 文件说明

| 文件 | 说明 |
|------|------|
| `react_agent.py` | 主程序，ReAct Agent 的创建和运行 |
| `custom_tools.py` | 自定义工具定义（天气、计算、知识库搜索等） |
| `requirements.txt` | Python 依赖 |
| `README.md` | 本文件 |

## 快速开始

### 1. 安装依赖

```bash
cd examples/langchain_react
pip install -r requirements.txt
```

### 2. 设置 API Key

```bash
# 使用 DeepSeek API
export DEEPSEEK_API_KEY="your-deepseek-api-key"
```

### 3. 运行

```bash
# 单次提问（使用默认问题）
python react_agent.py

# 自定义问题
python react_agent.py -q "深圳今天会下雨吗？"

# 交互模式
python react_agent.py -i

# 指定模型
python react_agent.py --model deepseek-reasoner
```

## 运行示例

```
$ python react_agent.py -q "北京和上海哪个城市更热？温度差多少？"

> Entering new AgentExecutor chain...

Thought: 我需要分别查询北京和上海的天气，然后比较温度
Action: get_weather
Action Input: {"city": "北京"}
Observation: 晴天，气温 28°C，空气质量良好，微风 2级

Thought: 已获取北京天气，现在查询上海
Action: get_weather
Action Input: {"city": "上海"}
Observation: 多云转阴，气温 32°C，湿度 75%，东南风 3级

Thought: 现在计算温度差
Action: calculate
Action Input: {"expression": "32 - 28"}
Observation: 32 - 28 = 4

Thought: 我已获得所有信息，可以给出最终答案
Final Answer: 上海更热。上海气温 32°C，北京气温 28°C，温度差 4°C。

> Finished chain.

🤖 助手: 上海更热。上海气温 32°C，北京气温 28°C，温度差 4°C。
```

## 自定义工具

在 `custom_tools.py` 中定义新工具：

```python
from langchain_core.tools import tool
from typing import Annotated

@tool
def my_tool(param: Annotated[str, "参数描述"]) -> str:
    """工具描述（LLM 通过这个描述理解工具用途）。"""
    # 实现逻辑
    return "结果"
```

然后在 `react_agent.py` 中导入并添加到 `ALL_TOOLS` 列表即可。

## 与 C++ Agent Engine 的对应关系

| LangChain 概念 | C++ Agent Engine | 文件 |
|----------------|------------------|------|
| `ChatOpenAI` | `OpenAIClient` | `engine/agent/llm_client_impl.h` |
| `@tool` | `ToolManager::registerTool()` | `engine/agent/tool_manager.h` |
| `AgentExecutor` | `ReActExecutor` | `engine/agent/react_executor.h` |
| `ChatPromptTemplate` | `PromptManager` | `engine/agent/prompt_manager.h` |
| `agent_scratchpad` | `AgentContext` | `engine/agent/agent_context.h` |
