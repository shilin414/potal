"""
AilyAgentAdapter — RuntimeAdapter implementation for Aily custom agents.

Resource mapping (official docs):
    external_resource_id   = agent_id
    AgentThread.remote_id  = session_id (lazy)
    Run.external_run_id    = agent_chat_id
    RuntimeAttachment      = agent_attachment_id
    RunArtifact            = agent_artifact_id

Streaming is transport only: after the SSE stream ends or breaks, the run
must be reconciled via get_chat_result before reaching a terminal state.
"""
from __future__ import annotations

import logging
from typing import AsyncIterator, Optional

from apps.catalog.providers import AILY_AGENT_CAPABILITIES
from apps.catalog.runtime import (
    ArtifactRef,
    ProviderAuthContext,
    RuntimeAdapter,
    StatusResult,
    StreamEvent,
    SubmitInput,
    SubmitResult,
    registry,
)
from integrations.aily.client import AilyClient
from integrations.aily.event_mapper import (
    extract_artifacts,
    extract_final_text,
    map_provider_status,
    parse_sse_line,
    sse_to_unified,
)
from integrations.aily.exceptions import (
    AilyCapabilityError,
    AilyError,
    AilyRateLimitError,
    AilyTimeoutError,
)

logger = logging.getLogger(__name__)


class AilyAgentAdapter(RuntimeAdapter):
    runtime_key = 'feishu_aily:agent'
    capabilities = AILY_AGENT_CAPABILITIES

    # Catalog descriptors (see RuntimeAdapter): the marketplace renders the
    # "新建智能体" form straight from these, so the Aily AgentID shape lives
    # next to the Aily code and nowhere else.
    display_label = '飞书 Aily 自定义智能体'
    resource_id_label = 'Agent ID'
    # Official docs: AgentID is read from the builder URL, e.g.
    # https://xxx.feishu.cn/ai/custom_agent/agent_4k6wf15ngw7wu/builder/persona
    resource_id_pattern = r'^agent_[0-9A-Za-z]+$'
    resource_id_hint = (
        '飞书智能体 AgentID，形如 agent_4k6wf15ngw7wu；'
        '在智能体编辑页的浏览器地址栏中获取')

    def __init__(self, client: Optional[AilyClient] = None):
        self.client = client or AilyClient()

    # ------------------------------------------------------------------
    # Input validation (front-line, before hitting Aily)
    # ------------------------------------------------------------------
    @staticmethod
    def validate_content(content_items: list[dict]) -> None:
        if not content_items:
            raise AilyCapabilityError('user_message.content must not be empty')
        if len(content_items) > 100:
            raise AilyCapabilityError('content items exceed 100')
        for item in content_items:
            if (item.get('type') or 'text') != 'text':
                raise AilyCapabilityError(
                    f'content type {item.get("type")!r} not supported '
                    '(only text)')
            if len(item.get('text') or '') > 10000:
                raise AilyCapabilityError('text exceeds 10000 chars')

    @staticmethod
    def validate_attachments(attachment_ids: list[str]) -> None:
        if len(attachment_ids) > 8:
            raise AilyCapabilityError('attachment ids exceed 8 per chat')

    # ------------------------------------------------------------------
    # Lifecycle
    # ------------------------------------------------------------------
    async def submit(self, submit: SubmitInput) -> SubmitResult:
        self.validate_content(submit.payload.get('content') or [])
        self.validate_attachments(submit.external_attachment_ids)
        data = await self.client.start_chat(
            submit.external_resource_id,
            submit.auth.token,
            content_items=submit.payload['content'],
            attachment_ids=submit.external_attachment_ids or None,
            stream=False,
            session_id=submit.session_id or '',
        )
        return SubmitResult(
            external_run_id=data.get('agent_chat_id') or '',
            session_id=data.get('session_id'),
            raw=data,
        )

    async def get_status(self, auth: ProviderAuthContext,
                         external_resource_id: str,
                         external_run_id: str) -> StatusResult:
        data = await self.client.get_chat_result(
            external_resource_id, auth.token, external_run_id)
        status = map_provider_status(data.get('status') or '')
        return StatusResult(
            external_run_id=external_run_id,
            provider_status=data.get('status') or '',
            finish_reason=data.get('finish_reason') or '',
            output={
                'text': extract_final_text(data),
                'artifacts': extract_artifacts(data),
                'raw_status': data.get('status'),
            },
            raw=data,
        )

    async def stream_events(self, submit: SubmitInput) -> AsyncIterator[StreamEvent]:
        """Stream a chat and yield unified events.

        The first yielded event carries agent_chat_id / session_id so the
        caller can persist them even if the stream later breaks.
        """
        self.validate_content(submit.payload.get('content') or [])
        self.validate_attachments(submit.external_attachment_ids)
        current_event = ''
        started = False
        async for line in self.client.stream_chat(
                submit.external_resource_id,
                submit.auth.token,
                content_items=submit.payload['content'],
                attachment_ids=submit.external_attachment_ids or None,
                session_id=submit.session_id or '',
        ):
            parsed = parse_sse_line(line)
            if parsed is None:
                continue
            if parsed['kind'] == 'event':
                current_event = parsed['name']
                continue
            data = parsed.get('data') or {}
            if not started:
                started = True
                # Surface run identity as soon as it appears.
                yield StreamEvent(
                    event_type='aily.stream.started',
                    payload={
                        'agent_chat_id': data.get('agent_chat_id') or '',
                        'session_id': data.get('session_id') or '',
                    })
            for event_type, payload in sse_to_unified(current_event, data):
                yield StreamEvent(event_type=event_type, payload=payload)

    async def cancel(self, auth: ProviderAuthContext, external_run_id: str) -> bool:
        raise AilyCapabilityError(
            'Aily custom agent API provides no cancel endpoint')

    async def upload_attachment(self, auth: ProviderAuthContext,
                                external_resource_id: str, *,
                                file_bytes: Optional[bytes] = None,
                                filename: str = '',
                                attachment_type: str = 'file',
                                doc_url: str = '') -> str:
        if attachment_type in ('image', 'file'):
            self._validate_file(filename, file_bytes, attachment_type)
        data = await self.client.upload_attachment(
            external_resource_id, auth.token,
            file_bytes=file_bytes, filename=filename,
            attachment_type=attachment_type, doc_url=doc_url)
        return data.get('agent_attachment_id') or ''

    # Official limits: file 40M (png/jpg/pdf), image 5M.
    ALLOWED_FILE_TYPES = {'png', 'jpg', 'jpeg', 'pdf'}

    @staticmethod
    def _validate_file(filename: str, file_bytes: Optional[bytes],
                       attachment_type: str) -> None:
        if file_bytes is None:
            raise AilyCapabilityError('file bytes required')
        ext = (filename.rsplit('.', 1)[-1] or '').lower()
        if ext not in AilyAgentAdapter.ALLOWED_FILE_TYPES:
            raise AilyCapabilityError(
                f'file type .{ext} not allowed (png/jpg/pdf only)')
        size = len(file_bytes)
        if attachment_type == 'image' and size > 5 * 1024 * 1024:
            raise AilyCapabilityError('image exceeds 5MB')
        if size > 40 * 1024 * 1024:
            raise AilyCapabilityError('file exceeds 40MB')

    async def resolve_artifact(self, auth: ProviderAuthContext,
                               external_resource_id: str,
                               external_artifact_id: str) -> ArtifactRef:
        data = await self.client.get_artifact(
            external_resource_id, auth.token, external_artifact_id)
        artifact = data.get('agent_artifact') or {}
        return ArtifactRef(
            external_artifact_id=artifact.get('artifact_id') or external_artifact_id,
            name=artifact.get('name') or '',
            url=artifact.get('url') or '',
        )

    async def build_auth(self, user,
                         identity_mode: str = 'user') -> ProviderAuthContext:
        """Resolve Studio session → Aily credential (UAT by default, §49)."""
        from integrations.aily import auth as aily_auth
        return await aily_auth.build_auth_context(
            user, identity_mode=identity_mode)

    async def check_visibility(self, auth: ProviderAuthContext,
                               external_resource_id: str) -> bool:
        if auth.identity_mode != 'user':
            raise AilyCapabilityError(
                'Aily visibility check requires user identity (UAT)')
        return await self.client.check_visibility(
            external_resource_id, auth.token, channel_type='web_sdk')


def register() -> None:
    registry.register(AilyAgentAdapter())
