"""
Async Aily custom-agent OpenAPI client.

Endpoints implemented exactly per the local official docs
(飞书开放平台文档/飞书aily/自定义智能体):

    POST   /aily/v1/agents/:agent_id/chats                     发起智能体对话
    GET    /aily/v1/agents/:agent_id/chats/:agent_chat_id       获取对话结果
    POST   /aily/v1/agents/:agent_id/sessions                   创建会话
    GET    /aily/v1/agents/:agent_id/sessions                   查询会话列表
    GET    /aily/v1/agents/:agent_id/sessions/:session_id       查询指定会话
    DELETE /aily/v1/agents/:agent_id/sessions/:session_id       删除会话
    POST   /aily/v1/agents/:agent_id/attachments                上传附件
    GET    /aily/v1/agents/:agent_id/artifacts/:artifact_id     下载智能体产物
    POST   /aily/v1/agents/:agent_id/agent_visibility/check     校验可见性

All calls require Authorization: Bearer <token>; chat/attachment/visibility
calls default to user identity (UAT) per project requirements.
"""
from __future__ import annotations

import logging
from typing import Any, AsyncIterator, Optional

import httpx

from django.conf import settings

from integrations.aily.exceptions import (
    AilyAuthError,
    AilyClientError,
    AilyError,
    AilyRateLimitError,
    AilyServerError,
    AilyTimeoutError,
)

logger = logging.getLogger(__name__)


def _sensitive_headers(headers: dict) -> dict:
    """Copy headers with Authorization masked for logging."""
    safe = dict(headers)
    if 'Authorization' in safe:
        safe['Authorization'] = 'Bearer ***'
    return safe


class AilyClient:
    """Thin async HTTP client for the Aily custom-agent OpenAPI."""

    def __init__(self, base_url: Optional[str] = None,
                 timeout: float | None = None):
        self.base_url = (base_url or settings.AILY_BASE_URL).rstrip('/')
        self._timeout = timeout or settings.AILY_STREAM_TIMEOUT_SECONDS
        self._client: Optional[httpx.AsyncClient] = None
        # Loop the cached client was created on. The singleton adapter is
        # shared across event loops (worker plane + per-request asyncio.run
        # in the web view); an httpx client bound to a closed loop throws
        # 'Event loop is closed' on close, so rebuild when the loop changes.
        self._loop_id: Optional[int] = None

    # ------------------------------------------------------------------
    # Transport
    # ------------------------------------------------------------------
    async def _http(self) -> httpx.AsyncClient:
        import asyncio
        loop_id = id(asyncio.get_running_loop())
        # _loop_id None = not yet bound to any loop (fresh or injected
        # client, e.g. tests' MockTransport): adopt it for this loop.
        if (self._client is None or self._client.is_closed
                or (self._loop_id is not None and self._loop_id != loop_id)):
            if self._client is not None and not self._client.is_closed \
                    and self._loop_id is not None:
                # Detach without awaiting: the old loop may already be gone.
                self._client._transport = None  # noqa: SLF001
                self._client._pool = None  # noqa: SLF001
            self._client = httpx.AsyncClient(
                base_url=self.base_url, timeout=self._timeout)
            self._loop_id = loop_id
        elif self._loop_id is None:
            self._loop_id = loop_id
        return self._client

    async def aclose(self) -> None:
        if self._client is not None and not self._client.is_closed:
            await self._client.aclose()

    async def __aenter__(self) -> 'AilyClient':
        return self

    async def __aexit__(self, *exc_info) -> None:
        await self.aclose()

    def _headers(self, token: str) -> dict:
        return {
            'Authorization': f'Bearer {token}',
            'Content-Type': 'application/json; charset=utf-8',
        }

    @staticmethod
    def _raise_for_response(response: httpx.Response) -> None:
        code = None
        try:
            body = response.json()
            code = body.get('code')
            msg = body.get('msg') or response.reason_phrase
        except Exception:
            body = None
            msg = response.reason_phrase or 'aily error'
        if response.status_code == 429:
            raise AilyRateLimitError(
                msg, code=code, http_status=response.status_code, raw=body or {})
        if response.status_code >= 500:
            raise AilyServerError(
                msg, code=code, http_status=response.status_code, raw=body or {})
        if response.status_code in (401, 403):
            raise AilyAuthError(
                msg, code=code, http_status=response.status_code, raw=body or {})
        if response.status_code >= 400:
            raise AilyClientError(
                msg, code=code, http_status=response.status_code, raw=body or {})
        if code is not None and code != 0:
            # Transport 2xx with non-zero business code.
            error_cls = AilyServerError if code == 50001 else AilyClientError
            raise error_cls(
                msg, code=code, http_status=response.status_code, raw=body or {})

    async def _request(self, method: str, path: str, token: str, *,
                       json_body: Optional[dict] = None,
                       params: Optional[dict] = None) -> dict:
        client = await self._http()
        try:
            response = await client.request(
                method, path, headers=self._headers(token),
                json=json_body, params=params)
        except httpx.TimeoutException as exc:
            raise AilyTimeoutError(str(exc)) from exc
        logger.debug(
            'aily %s %s -> %s (headers=%s)', method, path,
            response.status_code, _sensitive_headers(response.request.headers))
        self._raise_for_response(response)
        data = response.json().get('data', {})
        return data if isinstance(data, dict) else {}

    # ------------------------------------------------------------------
    # Chat (发起智能体对话)
    # ------------------------------------------------------------------
    async def start_chat(self, agent_id: str, token: str, *,
                         content_items: list[dict],
                         attachment_ids: Optional[list[str]] = None,
                         stream: bool = False,
                         session_id: str = '') -> dict:
        """POST /aily/v1/agents/:agent_id/chats

        content_items: [{'type': 'text', 'text': ...}] — max 100 items,
        text max 10000 chars, attachment ids max 8 (validated upstream too).
        Returns {'agent_chat_id': ..., 'session_id': ...}.
        """
        user_message: dict[str, Any] = {'content': content_items}
        if attachment_ids:
            user_message['agent_attachment_ids'] = attachment_ids
        body: dict[str, Any] = {'user_message': user_message}
        if stream:
            body['stream'] = True
        if session_id:
            body['session_id'] = session_id
        return await self._request(
            'POST', f'/aily/v1/agents/{agent_id}/chats', token, json_body=body)

    async def stream_chat(self, agent_id: str, token: str, *,
                          content_items: list[dict],
                          attachment_ids: Optional[list[str]] = None,
                          session_id: str = '') -> AsyncIterator[dict]:
        """Start a chat with stream=true and yield raw SSE events.

        Per docs the streaming connection times out after ~5 minutes; the
        caller must treat timeouts as transport events, not run failures,
        and reconcile via get_chat_result.
        """
        user_message: dict[str, Any] = {'content': content_items}
        if attachment_ids:
            user_message['agent_attachment_ids'] = attachment_ids
        body: dict[str, Any] = {
            'user_message': user_message, 'stream': True}
        if session_id:
            body['session_id'] = session_id
        client = await self._http()
        try:
            async with client.stream(
                'POST', f'/aily/v1/agents/{agent_id}/chats',
                headers=self._headers(token), json=body,
            ) as response:
                self._raise_for_response(response)
                async for line in response.aiter_lines():
                    line = line.strip()
                    if not line:
                        continue
                    yield line
        except httpx.TimeoutException as exc:
            raise AilyTimeoutError(str(exc)) from exc

    # ------------------------------------------------------------------
    # Chat result (获取对话结果) — final reconciliation
    # ------------------------------------------------------------------
    async def get_chat_result(self, agent_id: str, token: str,
                              agent_chat_id: str) -> dict:
        """GET /aily/v1/agents/:agent_id/chats/:agent_chat_id

        Returns {'content': [...], 'finish_reason': ..., 'status': ...};
        content items may carry agent_artifact_id + artifact_type.
        """
        return await self._request(
            'GET', f'/aily/v1/agents/{agent_id}/chats/{agent_chat_id}', token)

    # ------------------------------------------------------------------
    # Sessions (会话)
    # ------------------------------------------------------------------
    async def create_session(self, agent_id: str, token: str,
                             name: str = '') -> dict:
        """POST /aily/v1/agents/:agent_id/sessions — blank session."""
        body = {'name': name} if name else {}
        return await self._request(
            'POST', f'/aily/v1/agents/{agent_id}/sessions', token,
            json_body=body)

    async def list_sessions(self, agent_id: str, token: str, *,
                            page_size: int = 20,
                            page_token: str = '') -> dict:
        """GET /aily/v1/agents/:agent_id/sessions"""
        params: dict[str, Any] = {'page_size': page_size}
        if page_token:
            params['page_token'] = page_token
        return await self._request(
            'GET', f'/aily/v1/agents/{agent_id}/sessions', token,
            params=params)

    async def get_session(self, agent_id: str, token: str,
                          session_id: str) -> dict:
        """GET /aily/v1/agents/:agent_id/sessions/:session_id"""
        return await self._request(
            'GET', f'/aily/v1/agents/{agent_id}/sessions/{session_id}', token)

    async def delete_session(self, agent_id: str, token: str,
                             session_id: str) -> dict:
        """DELETE /aily/v1/agents/:agent_id/sessions/:session_id"""
        return await self._request(
            'DELETE', f'/aily/v1/agents/{agent_id}/sessions/{session_id}',
            token)

    # ------------------------------------------------------------------
    # Attachments (上传附件)
    # ------------------------------------------------------------------
    async def upload_attachment(self, agent_id: str, token: str, *,
                                file_bytes: Optional[bytes] = None,
                                filename: str = '',
                                attachment_type: str = 'file',
                                doc_url: str = '') -> dict:
        """POST /aily/v1/agents/:agent_id/attachments

        Per docs: type=image|file requires `file` (png/jpg/pdf, file <=40M,
        image <=5M); type=feishu_doc|bitable requires `doc_url`.
        Returns {'agent_attachment_id': ...}.
        """
        files = None
        data: dict[str, str] = {'type': attachment_type}
        if attachment_type in ('image', 'file'):
            if file_bytes is None:
                raise AilyClientError(
                    'file bytes required for image/file attachments')
            files = {'file': (filename or 'upload', file_bytes)}
        elif attachment_type in ('feishu_doc', 'bitable'):
            if not doc_url:
                raise AilyClientError(
                    'doc_url required for feishu_doc/bitable attachments')
            data['doc_url'] = doc_url
        else:
            raise AilyClientError(f'unsupported attachment type {attachment_type}')

        client = await self._http()
        try:
            response = await client.post(
                f'/aily/v1/agents/{agent_id}/attachments',
                headers={'Authorization': f'Bearer {token}'},
                data=data, files=files)
        except httpx.TimeoutException as exc:
            raise AilyTimeoutError(str(exc)) from exc
        logger.debug('aily upload attachment -> %s', response.status_code)
        self._raise_for_response(response)
        return response.json().get('data', {}) or {}

    # ------------------------------------------------------------------
    # Artifacts (下载智能体产物)
    # ------------------------------------------------------------------
    async def get_artifact(self, agent_id: str, token: str,
                           agent_artifact_id: str) -> dict:
        """GET /aily/v1/agents/:agent_id/artifacts/:agent_artifact_id

        Returns {'agent_artifact': {'artifact_id', 'name', 'url'}}; the URL
        is valid for 24 hours only.
        """
        return await self._request(
            'GET',
            f'/aily/v1/agents/{agent_id}/artifacts/{agent_artifact_id}', token)

    # ------------------------------------------------------------------
    # Visibility (校验可见性)
    # ------------------------------------------------------------------
    async def check_visibility(self, agent_id: str, token: str,
                               channel_type: str = 'web_sdk') -> bool:
        """POST /aily/v1/agents/:agent_id/agent_visibility/check

        UAT only per docs; channel_type currently only supports web_sdk.
        """
        data = await self._request(
            'POST', f'/aily/v1/agents/{agent_id}/agent_visibility/check',
            token, json_body={'channel_type': channel_type})
        return bool(data.get('visibility'))
