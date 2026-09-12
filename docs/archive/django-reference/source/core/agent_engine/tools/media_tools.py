"""Media generation tools.

Integrates with existing ImageProvider and VideoProvider to expose
image/video generation as tools in the AgentEngine tool framework.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from .base import BaseTool, ToolParameter, ToolResult

if TYPE_CHECKING:
    from ..image_provider import ImageProvider
    from ..video_provider import VideoProvider


class ImageGenerateTool(BaseTool):
    """Generate an image from a text prompt."""

    def __init__(self, provider: ImageProvider) -> None:
        self._provider = provider

    @property
    def name(self) -> str:
        return "generate_image"

    @property
    def description(self) -> str:
        return "Generate an image from a text description."

    @property
    def parameters(self) -> list[ToolParameter]:
        return [
            ToolParameter(
                name="prompt",
                type="string",
                description="Text description of the image to generate",
                required=True,
            ),
            ToolParameter(
                name="size",
                type="string",
                description="Image size (e.g., '1024x1024')",
                required=False,
            ),
        ]

    def execute(self, prompt: str, size: str | None = None, **kwargs) -> ToolResult:
        try:
            kwargs["size"] = size
            result = self._provider.generate(prompt, **kwargs)

            if result.success and result.url:
                structured = {
                    "url": result.url,
                    "revised_prompt": result.revised_prompt,
                }
                return ToolResult.success(
                    f"Image generated successfully.\nURL: {result.url}",
                    structured=structured,
                )
            elif result.error:
                return ToolResult.error(f"Image generation failed: {result.error}")
            else:
                return ToolResult.error("Image generation returned no result")

        except Exception as exc:
            return ToolResult.error(f"Image generation error: {exc}")


class VideoGenerateTool(BaseTool):
    """Generate a video from a text prompt (async task)."""

    def __init__(self, provider: VideoProvider) -> None:
        self._provider = provider

    @property
    def name(self) -> str:
        return "generate_video"

    @property
    def description(self) -> str:
        return "Generate a video from a text description. Returns a task_id for async processing."

    @property
    def parameters(self) -> list[ToolParameter]:
        return [
            ToolParameter(
                name="prompt",
                type="string",
                description="Text description of the video to generate",
                required=True,
            ),
        ]

    def execute(self, prompt: str, **kwargs) -> ToolResult:
        try:
            task = self._provider.submit(prompt, **kwargs)

            structured = {
                "task_id": task.task_id,
                "status": task.status,
            }
            return ToolResult.success(
                f"Video generation task submitted.\nTask ID: {task.task_id}\nStatus: {task.status}",
                structured=structured,
            )

        except Exception as exc:
            return ToolResult.error(f"Video generation error: {exc}")


class MediaTools:
    """Factory for creating media tools."""

    @staticmethod
    def create_image_tool(provider: ImageProvider) -> ImageGenerateTool:
        return ImageGenerateTool(provider)

    @staticmethod
    def create_video_tool(provider: VideoProvider) -> VideoGenerateTool:
        return VideoGenerateTool(provider)