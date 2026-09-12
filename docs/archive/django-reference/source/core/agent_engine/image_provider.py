from __future__ import annotations

import time

import requests

from .models import ImageConfig, ImageResponse


class ImageProvider:
    """OpenAI-compatible image generation client.

    Supports DALL-E 3 and any provider that follows the
    POST /images/generations endpoint format.
    """

    def __init__(self, config: ImageConfig) -> None:
        self._config = config

    def generate(self, prompt: str, **kwargs) -> ImageResponse:
        """Generate an image from a text prompt.

        Args:
            prompt: Text description of the desired image.
            **kwargs: Override size, quality, model per request.

        Returns:
            ImageResponse with url or base64 on success.
        """
        url = f"{self._config.base_url.rstrip('/')}/images/generations"
        headers = {
            "Authorization": f"Bearer {self._config.api_key}",
            "Content-Type": "application/json",
        }
        body = {
            "model": kwargs.get("model", self._config.model),
            "prompt": prompt,
            "n": 1,
            "size": kwargs.get("size", self._config.size),
            "quality": kwargs.get("quality", self._config.quality),
            "response_format": "url",
        }

        last_error: Exception | None = None
        for attempt in range(self._config.max_retries + 1):
            try:
                resp = requests.post(
                    url, json=body, headers=headers,
                    timeout=self._config.timeout,
                )
                if not resp.ok:
                    return ImageResponse(
                        success=False,
                        error=f"HTTP {resp.status_code}: {resp.text[:300]}",
                    )
                return self._parse_response(resp.json())
            except (requests.ConnectionError, requests.Timeout) as exc:
                last_error = exc
                if attempt < self._config.max_retries:
                    time.sleep(attempt + 1)

        return ImageResponse(
            success=False,
            error=str(last_error),
        )

    def _parse_response(self, data: dict) -> ImageResponse:
        try:
            image_data = data["data"][0]
            return ImageResponse(
                url=image_data.get("url"),
                base64=image_data.get("b64_json"),
                revised_prompt=image_data.get("revised_prompt"),
            )
        except (KeyError, IndexError, TypeError) as exc:
            return ImageResponse(
                success=False,
                error=f"Failed to parse image response: {exc}",
            )
