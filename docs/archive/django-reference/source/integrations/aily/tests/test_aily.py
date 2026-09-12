import json

import httpx
import pytest

from apps.catalog.runtime import ProviderAuthContext, SubmitInput
from integrations.aily.agent_adapter import AilyAgentAdapter
from integrations.aily.client import AilyClient
from integrations.aily.event_mapper import (
    extract_artifacts,
    extract_final_text,
    map_provider_status,
    normalized_artifact_type,
    sse_to_unified,
)
from integrations.aily.exceptions import (
    AilyCapabilityError,
    AilyRateLimitError,
)


@pytest.mark.asyncio
async def test_start_chat_matches_official_request_contract(settings):
    captured = {}

    def handler(request: httpx.Request):
        captured['method'] = request.method
        captured['path'] = request.url.path
        captured['authorization'] = request.headers['Authorization']
        captured['body'] = json.loads(request.content)
        return httpx.Response(200, json={
            'code': 0,
            'msg': 'success',
            'data': {
                'agent_chat_id': 'chat_1',
                'session_id': 'conversation_1',
            },
        })

    client = AilyClient(base_url='https://open.feishu.cn/open-apis')
    client._client = httpx.AsyncClient(
        transport=httpx.MockTransport(handler),
        base_url='https://open.feishu.cn/open-apis')
    client._loop_id = None  # let _http() adopt the injected mock
    try:
        result = await client.start_chat(
            'agent_1', 'uat-secret',
            content_items=[{'type': 'text', 'text': 'hello'}],
            attachment_ids=['attachment_1'],
            session_id='conversation_existing')
    finally:
        await client.aclose()

    assert captured['method'] == 'POST'
    assert captured['path'] == '/open-apis/aily/v1/agents/agent_1/chats'
    assert captured['authorization'] == 'Bearer uat-secret'
    assert captured['body'] == {
        'user_message': {
            'content': [{'type': 'text', 'text': 'hello'}],
            'agent_attachment_ids': ['attachment_1'],
        },
        'session_id': 'conversation_existing',
    }
    assert result['agent_chat_id'] == 'chat_1'


@pytest.mark.asyncio
async def test_client_maps_http_429_to_rate_limit():
    def handler(request):
        return httpx.Response(429, json={'code': 2700001, 'msg': 'limited'})

    client = AilyClient(base_url='https://example.test/open-apis')
    client._client = httpx.AsyncClient(
        transport=httpx.MockTransport(handler),
        base_url='https://example.test/open-apis')
    client._loop_id = None  # let _http() adopt the injected mock
    try:
        with pytest.raises(AilyRateLimitError):
            await client.get_chat_result('agent_1', 'token', 'chat_1')
    finally:
        await client.aclose()


def test_result_mapping_extracts_text_artifacts_and_status():
    result = {
        'content': [
            {'type': 'text', 'text': '完成',
             'agent_artifact_id': 'artifact_1',
             'artifact_type': 'sandbox_file'},
        ],
        'status': 'Completed',
        'finish_reason': 'stop',
    }

    assert extract_final_text(result) == '完成'
    assert extract_artifacts(result)[0]['external_artifact_id'] == 'artifact_1'
    assert normalized_artifact_type('sandbox_file') == 'file'
    assert map_provider_status('Completed') == 'succeeded'


def test_adapter_enforces_official_attachment_limits():
    adapter = AilyAgentAdapter()

    with pytest.raises(AilyCapabilityError, match='exceed 8'):
        adapter.validate_attachments([str(i) for i in range(9)])
    with pytest.raises(AilyCapabilityError, match='png/jpg/pdf'):
        adapter._validate_file('malware.exe', b'x', 'file')
    with pytest.raises(AilyCapabilityError, match='5MB'):
        adapter._validate_file(
            'large.png', b'x' * (5 * 1024 * 1024 + 1), 'image')


def test_sse_delta_extracts_text_from_nested_content_object():
    """Real-world Aily SSE wraps text as {'text': {'text': ..., 'type': ...}}."""
    events = sse_to_unified('message', {
        'text': {'text': '你好', 'type': 'content'}})
    assert ('content.delta', {'text': '你好'}) in events

    # Plain-string shape (docs-style) still works.
    events = sse_to_unified('delta', {'text': 'plain'})
    assert ('content.delta', {'text': 'plain'}) in events

    # Non-text values never produce a delta.
    assert sse_to_unified('message', {'text': {'type': 'content'}}) == []


def test_extract_artifacts_pairs_markdown_filename_across_entries():
    """Aily splits a file into a text entry (markdown) + an artifact entry."""
    result = {
        'content': [
            {'type': 'text', 'text': '说明'},
            {'type': 'text',
             'text': '图片已生成：\n\n![橙色图](artifacts/orange_test_image.png)'},
            {'type': 'image', 'text': '',
             'agent_artifact_id': 'artifact_1', 'artifact_type': 'sandbox_file'},
        ],
        'status': 'Completed',
    }
    extracted = extract_artifacts(result)
    assert extracted[0]['name'] == 'orange_test_image.png'

    # Nested gallery paths yield the trailing filename too.
    nested = {
        'content': [
            {'type': 'text',
             'text': '![绿色](artifacts/test_image/gallery/green_test.png)'},
            {'type': 'file', 'text': '',
             'agent_artifact_id': 'artifact_2', 'artifact_type': 'sandbox_file'},
        ],
    }
    assert extract_artifacts(nested)[0]['name'] == 'green_test.png'
