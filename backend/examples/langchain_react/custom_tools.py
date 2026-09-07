"""
Custom Tools for LangChain ReAct Agent
=======================================

演示如何定义自定义工具，供 ReAct Agent 调用。
工具定义遵循 LangChain @tool 装饰器模式。

工具分类:
    - 天气查询工具
    - 数学计算工具
    - 知识库搜索工具
    - 时间查询工具
"""

import datetime
from typing import Annotated

from langchain_core.tools import tool


# ---------------------------------------------------------------------------
# 天气工具
# ---------------------------------------------------------------------------

@tool
def get_weather(city: Annotated[str, "城市名称，如：北京、上海、深圳"]) -> str:
    """获取指定城市的天气信息。

    Args:
        city: 城市名称

    Returns:
        天气信息字符串
    """
    weather_db = {
        "北京": "晴天，气温 28°C，空气质量良好，微风 2级",
        "上海": "多云转阴，气温 32°C，湿度 75%，东南风 3级",
        "深圳": "雷阵雨，气温 30°C，湿度 85%，南风 4级",
        "广州": "大雨，气温 29°C，湿度 90%，西南风 5级",
        "成都": "阴天，气温 25°C，湿度 60%，微风 1级",
        "杭州": "小雨，气温 27°C，湿度 80%，东风 2级",
        "武汉": "晴转多云，气温 33°C，湿度 65%，南风 2级",
        "南京": "多云，气温 31°C，湿度 55%，东风 3级",
    }
    return weather_db.get(city, f"暂无 {city} 的天气数据，请尝试其他城市")


@tool
def get_weather_comparison(
    city1: Annotated[str, "第一个城市名称"],
    city2: Annotated[str, "第二个城市名称"],
) -> str:
    """对比两个城市的天气信息。

    Args:
        city1: 第一个城市
        city2: 第二个城市城市

    Returns:
        天气对比字符串
    """
    w1 = get_weather.invoke({"city": city1})
    w2 = get_weather.invoke({"city": city2})
    return f"{city1}: {w1}\n{city2}: {w2}"


# ---------------------------------------------------------------------------
# 数学计算工具
# ---------------------------------------------------------------------------

@tool
def calculate(expression: Annotated[str, "数学表达式，如：'2 + 3 * 4'"]) -> str:
    """计算数学表达式。支持加减乘除和括号。

    Args:
        expression: 数学表达式字符串

    Returns:
        计算结果字符串
    """
    try:
        allowed_chars = set("0123456789+-*/.() ")
        if not all(c in allowed_chars for c in expression):
            return "错误：表达式包含不允许的字符，仅支持数字和 + - * / ( )"
        result = eval(expression)  # noqa: S307
        return f"{expression} = {result}"
    except ZeroDivisionError:
        return "计算错误：除数不能为零"
    except Exception as e:
        return f"计算错误：{e}"


@tool
def unit_convert(
    value: Annotated[float, "待转换的数值"],
    from_unit: Annotated[str, "原始单位 (celsius/fahrenheit/km/miles/kg/lb)"],
    to_unit: Annotated[str, "目标单位 (celsius/fahrenheit/km/miles/kg/lb)"],
) -> str:
    """单位转换工具。支持温度、距离、重量单位转换。

    Args:
        value: 待转换的数值
        from_unit: 原始单位
        to_unit: 目标单位

    Returns:
        转换结果字符串
    """
    # 温度转换
    temp_units = {"celsius", "fahrenheit"}
    if from_unit in temp_units and to_unit in temp_units:
        if from_unit == "celsius" and to_unit == "fahrenheit":
            result = value * 9 / 5 + 32
            return f"{value}°C = {result:.1f}°F"
        elif from_unit == "fahrenheit" and to_unit == "celsius":
            result = (value - 32) * 5 / 9
            return f"{value}°F = {result:.1f}°C"

    # 距离转换
    dist_units = {"km", "miles"}
    if from_unit in dist_units and to_unit in dist_units:
        if from_unit == "km" and to_unit == "miles":
            result = value * 0.621371
            return f"{value} km = {result:.2f} miles"
        elif from_unit == "miles" and to_unit == "km":
            result = value * 1.60934
            return f"{value} miles = {result:.2f} km"

    # 重量转换
    weight_units = {"kg", "lb"}
    if from_unit in weight_units and to_unit in weight_units:
        if from_unit == "kg" and to_unit == "lb":
            result = value * 2.20462
            return f"{value} kg = {result:.2f} lb"
        elif from_unit == "lb" and to_unit == "kg":
            result = value * 0.453592
            return f"{value} lb = {result:.2f} kg"

    return f"不支持从 {from_unit} 到 {to_unit} 的转换"


# ---------------------------------------------------------------------------
# 知识库搜索工具
# ---------------------------------------------------------------------------

@tool
def search_knowledge(query: Annotated[str, "搜索关键词或问题"]) -> str:
    """在知识库中搜索相关信息。

    Args:
        query: 搜索关键词

    Returns:
        搜索结果字符串
    """
    knowledge_db = {
        "Python": (
            "Python 是一种广泛使用的高级编程语言，由 Guido van Rossum 于 1991 年发布。"
            "它支持多种编程范式，包括面向对象、函数式和过程式编程。"
            "Python 以其清晰的语法和强大的标准库而闻名。"
        ),
        "LangChain": (
            "LangChain 是一个用于构建 LLM 应用的框架，提供了链式调用、"
            "工具集成、记忆管理等核心能力。支持 ReAct、Plan-and-Execute 等多种 Agent 模式。"
            "核心概念包括：Chains, Agents, Tools, Memory, Prompts。"
        ),
        "ReAct": (
            "ReAct (Reasoning + Acting) 是一种将推理和行动结合的范式，"
            "由 Yao et al. 在 2022 年提出。"
            "Agent 在每一步先推理(Thought)，然后采取行动(Action)，"
            "再观察结果(Observation)，循环往复直到完成任务。"
            "相比纯 Chain-of-Thought，ReAct 能与外部环境交互获取真实信息。"
        ),
        "Agent": (
            "Agent 是能够自主决策和执行任务的 AI 系统。"
            "核心组件包括：LLM（大脑）、Tools（手脚）、Memory（记忆）、Planning（规划）。"
            "LangChain 提供了多种 Agent 实现，包括 ReAct Agent、Plan-and-Execute Agent 等。"
        ),
    }

    results = []
    for key, value in knowledge_db.items():
        if key.lower() in query.lower() or query.lower() in key.lower():
            results.append(f"【{key}】{value}")

    if results:
        return "\n\n".join(results)
    return f"未找到与 '{query}' 相关的信息，请尝试其他关键词"


# ---------------------------------------------------------------------------
# 时间工具
# ---------------------------------------------------------------------------

@tool
def get_current_time(
    timezone_name: Annotated[str, "时区名称，如：Asia/Shanghai, US/Eastern"] = "Asia/Shanghai",
) -> str:
    """获取当前时间。

    Args:
        timezone_name: 时区名称

    Returns:
        当前时间字符串
    """
    try:
        import zoneinfo
        tz = zoneinfo.ZoneInfo(timezone_name)
        now = datetime.datetime.now(tz)
        return f"当前时间 ({timezone_name}): {now.strftime('%Y-%m-%d %H:%M:%S %A')}"
    except Exception:
        now = datetime.datetime.now()
        return f"当前时间 (本地): {now.strftime('%Y-%m-%d %H:%M:%S %A')}"


# ---------------------------------------------------------------------------
# 工具注册表 - 汇总所有工具
# ---------------------------------------------------------------------------

BASIC_TOOLS = [get_weather, calculate, search_knowledge]

ALL_TOOLS = [
    # 天气
    get_weather,
    get_weather_comparison,
    # 计算
    calculate,
    unit_convert,
    # 知识库
    search_knowledge,
    # 时间
    get_current_time,
]


def get_tools_by_name(names: list[str]) -> list:
    """按名称获取工具列表。

    Args:
        names: 工具名称列表

    Returns:
        匹配的工具列表
    """
    tool_map = {t.name: t for t in ALL_TOOLS}
    return [tool_map[n] for n in names if n in tool_map]
