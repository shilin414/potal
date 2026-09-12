"""Unit tests for AgentEngine streaming + stateless completion (no Django)."""
import json
import unittest
from unittest.mock import MagicMock, patch

from core.agent_engine.models import EngineConfig
from core.agent_engine.provider import LLMProvider
from core.agent_engine.engine import AgentEngine


def _sse_response_lines(objs, status_code=200, reason="OK"):
    """Build a fake requests.Response-like object that emits SSE data lines."""
    lines = [b"data: " + json.dumps(o).encode("utf-8") for o in objs]
    lines.append(b"data: [DONE]")
    resp = MagicMock()
    resp.ok = status_code < 400
    resp.status_code = status_code
    resp.text = reason
    resp.iter_lines.return_value = iter(lines)
    return resp


def _sse_response(chunks, status_code=200, reason="OK"):
    """SSE response where each chunk string is a content delta."""
    return _sse_response_lines(
        [{"choices": [{"delta": {"content": c}}]} for c in chunks],
        status_code=status_code, reason=reason,
    )


class LLMProviderCompleteStreamTest(unittest.TestCase):
    def test_yields_content_deltas_until_done(self):
        provider = LLMProvider(EngineConfig(api_key="k", base_url="https://x/v1"))
        with patch("core.agent_engine.provider.requests.post",
                   return_value=_sse_response(["你好", "，世界"])):
            chunks = list(provider.complete_stream([{"role": "user", "content": "hi"}]))
        self.assertEqual(chunks, ["你好", "，世界"])

    def test_raises_on_http_error(self):
        provider = LLMProvider(EngineConfig(api_key="k", base_url="https://x/v1"))
        with patch("core.agent_engine.provider.requests.post",
                   return_value=_sse_response([], status_code=500, reason="boom")):
            with self.assertRaises(RuntimeError):
                list(provider.complete_stream([{"role": "user", "content": "hi"}]))

    def test_skips_role_only_delta_and_usage_chunk(self):
        provider = LLMProvider(EngineConfig(api_key="k", base_url="https://x/v1"))
        sse = _sse_response_lines([
            {"choices": [{"delta": {"role": "assistant"}}]},
            {"choices": [], "usage": {}},
            {"choices": [{"delta": {"content": "hi"}}]},
        ])
        with patch("core.agent_engine.provider.requests.post", return_value=sse):
            self.assertEqual(
                list(provider.complete_stream([{"role": "user", "content": "hi"}])),
                ["hi"],
            )

    def test_done_only_stream_yields_nothing(self):
        provider = LLMProvider(EngineConfig(api_key="k", base_url="https://x/v1"))
        with patch("core.agent_engine.provider.requests.post",
                   return_value=_sse_response([])):
            self.assertEqual(
                list(provider.complete_stream([{"role": "user", "content": "hi"}])),
                [],
            )

    def test_raises_on_provider_error_object(self):
        provider = LLMProvider(EngineConfig(api_key="k", base_url="https://x/v1"))
        sse = _sse_response_lines([{"error": {"message": "rate limited"}}])
        with patch("core.agent_engine.provider.requests.post", return_value=sse):
            with self.assertRaises(RuntimeError):
                list(provider.complete_stream([{"role": "user", "content": "hi"}]))

    @patch("core.agent_engine.provider.time.sleep", lambda _s: None)
    def test_retries_connection_error_then_succeeds(self):
        import requests as _requests
        provider = LLMProvider(EngineConfig(api_key="k", base_url="https://x/v1"))
        with patch("core.agent_engine.provider.requests.post",
                   side_effect=[_requests.ConnectionError("boom"),
                                _sse_response(["ok"])]):
            self.assertEqual(
                list(provider.complete_stream([{"role": "user", "content": "hi"}])),
                ["ok"],
            )


class AgentEngineAdapterTest(unittest.TestCase):
    def test_complete_delegates_to_injected_adapter(self):
        adapter = MagicMock()
        adapter.name = "test"
        adapter.complete.return_value = MagicMock(
            success=True,
            usage=MagicMock(total_tokens=5),
            error=None,
        )
        messages = [
            {"role": "system", "content": "system"},
            {"role": "user", "content": "hi"},
        ]

        result = AgentEngine(adapter=adapter).complete(messages)

        self.assertTrue(result.success)
        self.assertEqual(result.usage.total_tokens, 5)
        adapter.complete.assert_called_once_with(messages, working_directory="")
