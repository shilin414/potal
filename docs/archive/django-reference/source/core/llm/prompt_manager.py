"""
Prompt Manager
"""
from typing import List, Dict


class PromptManager:
    """提示词管理器"""

    SYSTEM_PROMPT = """你是一个专业的视频创作助手，可以帮助用户：
1. 规划视频内容和结构
2. 提供创意建议
3. 优化文案和脚本
4. 回答视频创作相关问题

请用友好、专业的语气与用户交流。"""

    @staticmethod
    def build_messages(
        history: List[Dict[str, str]],
        user_message: str,
        system_prompt: str = None
    ) -> List[Dict[str, str]]:
        """构建消息列表"""
        messages = []

        # 添加系统提示
        if system_prompt:
            messages.append({"role": "system", "content": system_prompt})
        else:
            messages.append({"role": "system", "content": PromptManager.SYSTEM_PROMPT})

        # 添加历史消息
        for msg in history[-10:]:  # 只保留最近10条消息
            messages.append({
                "role": msg["role"],
                "content": msg["content"]
            })

        # 添加当前用户消息
        messages.append({"role": "user", "content": user_message})

        return messages
