"""
AilyDispatcher — queue, rate limit, concurrency, retry, backoff.

Runs inside the aily worker plane. For each claimed Run it:
  1. resolves the ProviderAuthContext (UAT for user-mode bindings),
  2. streams the chat (interactive) or submits async (background),
  3. reconciles the final state via get_chat_result,
  4. persists events/artifacts and updates the Conversation's AgentThread.
"""
from __future__ import annotations

import asyncio
import logging

from asgiref.sync import sync_to_async
from django.conf import settings

from apps.catalog.runtime import (
    ProviderAuthContext,
    SubmitInput,
    registry,
)
from apps.conversations.models import AgentThread, Message
from apps.execution.models import Run, RunArtifact
from apps.execution.services import (
    EVENT_ARTIFACT_DISCOVERED,
    EVENT_CONTENT_DELTA,
    RunManager,
)
from integrations.aily import auth as aily_auth
from integrations.aily.agent_adapter import AilyAgentAdapter
from integrations.aily.event_mapper import (
    extract_artifacts,
    extract_final_text,
    map_provider_status,
)
from integrations.aily.exceptions import (
    AilyError,
    AilyRateLimitError,
    AilyTimeoutError,
)
from integrations.aily.rate_limit import chats_limiter, polls_limiter

logger = logging.getLogger(__name__)


class AilyDispatcher:
    """Executes claimed Aily agent runs on the asyncio worker plane."""

    def __init__(self, redis, adapter: AilyAgentAdapter | None = None):
        self.redis = redis
        self.adapter = adapter or registry.resolve('feishu_aily', 'agent')
        self.limiter = chats_limiter(redis)
        self.poll_limiter = polls_limiter(redis)

    # ------------------------------------------------------------------
    # Entry
    # ------------------------------------------------------------------
    async def execute(self, run: Run) -> None:
        binding = run.runtime_binding
        snapshot = run.runtime_snapshot or {}
        agent_id = snapshot.get('external_resource_id') or (
            binding.external_resource_id if binding else '')
        if not agent_id:
            await self._fail(run, 'aily_no_agent_id', 'missing agent_id')
            return

        identity_mode = snapshot.get('identity_mode') or 'user'
        try:
            auth = await aily_auth.build_auth_context(
                run.user, identity_mode=identity_mode)
        except Exception as exc:
            await self._fail(run, 'aily_auth_error', str(exc))
            return

        stream = snapshot.get('execution_mode', 'interactive') == 'interactive'
        try:
            await self.limiter.acquire_async()
            if stream:
                await self._execute_streaming(run, auth, agent_id)
            else:
                await self._execute_background(run, auth, agent_id)
        except AilyRateLimitError:
            await self._retry_or_fail(run, 'aily_rate_limit')
        except AilyTimeoutError:
            # SSE transport timeout: reconcile instead of failing.
            await self._reconcile(run, auth, agent_id)
        except AilyError as exc:
            if exc.retryable:
                await self._retry_or_fail(run, exc.error_code)
            else:
                await self._fail(run, exc.error_code, exc.message)
        except Exception as exc:  # noqa: BLE001
            logger.exception('aily run %s crashed', run.pk)
            await self._fail(run, 'aily_internal_error', str(exc))

    # ------------------------------------------------------------------
    # Streaming (interactive)
    # ------------------------------------------------------------------
    async def _execute_streaming(self, run: Run, auth: ProviderAuthContext,
                                 agent_id: str) -> None:
        thread = await self._thread(run)
        submit = self._build_submit(
            run, auth, agent_id, stream=True,
            session_id=thread.remote_id if thread else '')

        external_run_id = ''
        session_id = thread.remote_id if thread else ''
        async for event in self.adapter.stream_events(submit):
            if event.event_type == 'aily.stream.started':
                payload = event.payload or {}
                external_run_id = payload.get('agent_chat_id') or ''
                new_session = payload.get('session_id') or ''
                await self._bind_thread_and_run(
                    run, thread, external_run_id, new_session or session_id)
                continue
            if event.event_type == EVENT_CONTENT_DELTA:
                await sync_to_async(
                    RunManager.append_event, thread_sensitive=True)(
                    run, EVENT_CONTENT_DELTA, event.payload)
            elif event.event_type == EVENT_ARTIFACT_DISCOVERED:
                await self._record_artifact(run, event.payload)

        # Transport finished (or broke): final authority is get_chat_result.
        await self._reconcile(run, auth, agent_id, external_run_id=external_run_id)

    # ------------------------------------------------------------------
    # Background (poll)
    # ------------------------------------------------------------------
    async def _execute_background(self, run: Run, auth: ProviderAuthContext,
                                  agent_id: str) -> None:
        thread = await self._thread(run)
        submit = self._build_submit(
            run, auth, agent_id, stream=False,
            session_id=thread.remote_id if thread else '')
        result = await self.adapter.submit(submit)
        await Run.objects.filter(pk=run.pk).aupdate(
            external_run_id=result.external_run_id)
        await run.arefresh_from_db()
        if thread:
            await self._bind_thread_and_run(
                run, thread, result.external_run_id,
                result.session_id or thread.remote_id)
        await self._poll_until_terminal(run, auth, agent_id)

    async def _poll_until_terminal(self, run: Run, auth: ProviderAuthContext,
                                   agent_id: str) -> None:
        backoffs = settings.AILY_POLL_BACKOFF_SECONDS or [1, 2, 3, 5]
        attempt = 0
        timeout_seconds = int(
            (run.runtime_snapshot or {}).get('timeout_seconds') or 300)
        deadline = asyncio.get_running_loop().time() + timeout_seconds
        while True:
            if asyncio.get_running_loop().time() >= deadline:
                await self._fail(
                    run, 'aily_poll_timeout',
                    f'chat did not finish within {timeout_seconds}s')
                return
            await asyncio.sleep(backoffs[min(attempt, len(backoffs) - 1)])
            attempt += 1
            await self.poll_limiter.acquire_async()
            status = await self.adapter.get_status(
                auth, agent_id, run.external_run_id)
            await sync_to_async(
                RunManager.append_event, thread_sensitive=True)(
                run, 'run.poll', {
                    'provider_status': status.provider_status})
            mapped = map_provider_status(status.provider_status)
            if mapped in ('succeeded', 'failed', 'cancelled'):
                await self._finalize_from_result(run, status.raw)
                return

    # ------------------------------------------------------------------
    # Final reconciliation
    # ------------------------------------------------------------------
    async def _reconcile(self, run: Run, auth: ProviderAuthContext,
                         agent_id: str, external_run_id: str = '') -> None:
        chat_id = run.external_run_id or external_run_id
        if not chat_id:
            await self._fail(run, 'aily_no_chat_id',
                             'stream ended without agent_chat_id')
            return
        await self.poll_limiter.acquire_async()
        status = await self.adapter.get_status(auth, agent_id, chat_id)
        mapped = map_provider_status(status.provider_status)
        if mapped in ('succeeded', 'failed', 'cancelled'):
            await self._finalize_from_result(run, status.raw)
        else:
            await self._poll_until_terminal(run, auth, agent_id)

    async def _finalize_from_result(self, run: Run, chat_result: dict) -> None:
        final_text = extract_final_text(chat_result)
        artifacts = extract_artifacts(chat_result)
        for artifact in artifacts:
            await self._record_artifact(run, artifact)

        mapped = map_provider_status(chat_result.get('status') or '')
        if mapped == 'failed':
            await sync_to_async(RunManager.finish, thread_sensitive=True)(
                run, Run.Status.FAILED,
                provider_status=chat_result.get('status') or '',
                finish_reason=chat_result.get('finish_reason') or '',
                error_code='aily_provider_failed',
                error_message=chat_result.get('msg') or '')
        elif mapped == 'cancelled':
            await sync_to_async(RunManager.finish, thread_sensitive=True)(
                run, Run.Status.CANCELLED,
                output={'text': final_text, 'status': chat_result.get('status')},
                provider_status=chat_result.get('status') or '',
                finish_reason=chat_result.get('finish_reason') or '')
        elif mapped == 'succeeded':
            await sync_to_async(RunManager.finish, thread_sensitive=True)(
                run, Run.Status.SUCCEEDED,
                output={'text': final_text, 'status': chat_result.get('status')},
                provider_status=chat_result.get('status') or '',
                finish_reason=chat_result.get('finish_reason') or '')

        if final_text and run.conversation_id:
            def _persist_assistant_message():
                artifact_summaries = [
                    {'artifact_id': str(a.pk), 'name': a.name,
                     'normalized_type': a.normalized_type}
                    for a in run.artifacts.order_by('created_at')
                ]
                Message.objects.create(
                    conversation_id=run.conversation_id,
                    role='assistant',
                    content=final_text,
                    metadata={
                        'run_id': str(run.pk),
                        'provider': 'feishu_aily',
                        **({'artifacts': artifact_summaries}
                           if artifact_summaries else {}),
                    })
            await sync_to_async(_persist_assistant_message, thread_sensitive=True)()

    # ------------------------------------------------------------------
    # Helpers
    # ------------------------------------------------------------------
    def _build_submit(self, run: Run, auth: ProviderAuthContext,
                      agent_id: str, *, stream: bool,
                      session_id: str = '') -> SubmitInput:
        return SubmitInput(
            run_id=str(run.pk),
            auth=auth,
            external_resource_id=agent_id,
            payload=run.input,
            session_id=session_id,
            external_attachment_ids=self._attachment_ids(run),
            stream=stream,
            timeout_seconds=run.runtime_snapshot.get('timeout_seconds', 300),
        )

    @staticmethod
    async def _thread(run: Run) -> AgentThread | None:
        if not run.conversation_id:
            return None
        thread, _created = await AgentThread.objects.aget_or_create(
            conversation_id=run.conversation_id,
            defaults={
                'provider': 'feishu_aily',
                'auth_mode': (
                    run.runtime_snapshot.get('identity_mode') or 'user'),
                'auth_subject_key': str(run.user_id or ''),
            },
        )
        expected_mode = run.runtime_snapshot.get('identity_mode') or 'user'
        expected_subject = str(run.user_id or '')
        if (thread.provider != 'feishu_aily'
                or thread.auth_mode != expected_mode
                or thread.auth_subject_key != expected_subject):
            raise AilyError(
                'conversation thread identity/provider mismatch')
        return thread

    @staticmethod
    def _attachment_ids(run: Run) -> list[str]:
        ids = (run.input or {}).get('agent_attachment_ids') or []
        return [str(i) for i in ids]

    async def _bind_thread_and_run(self, run: Run, thread: AgentThread | None,
                                   external_run_id: str,
                                   session_id: str) -> None:
        updates = {}
        if external_run_id and not run.external_run_id:
            updates['external_run_id'] = external_run_id
        if updates:
            await Run.objects.filter(pk=run.pk).aupdate(**updates)
            await run.arefresh_from_db()
        if thread is not None:
            if session_id and thread.remote_id != session_id:
                await AgentThread.objects.filter(pk=thread.pk).aupdate(
                    remote_id=session_id, status=AgentThread.Status.ACTIVE)
                thread.remote_id = session_id
                thread.status = AgentThread.Status.ACTIVE

    async def _record_artifact(self, run: Run, payload: dict) -> None:
        external_id = payload.get('external_artifact_id') or ''
        if not external_id:
            return
        provider_type = payload.get('provider_artifact_type') or ''
        # Aily content entries embed artifacts in markdown as relative
        # `artifacts/<name>` refs; the filename is what links the message
        # text to the artifact card, so default it from the external id.
        name = payload.get('name') or ''
        from integrations.aily.event_mapper import normalized_artifact_type
        artifact, _created = await RunArtifact.objects.aupdate_or_create(
            run=run, external_artifact_id=external_id,
            defaults={
                'provider': 'feishu_aily',
                'provider_artifact_type': provider_type,
                'name': name,
                'normalized_type': normalized_artifact_type(provider_type),
                'resolution_status': RunArtifact.ResolutionStatus.PENDING,
            })
        # The local artifact id is the clickable handle; the external id is
        # only a long-term provider reference (URLs are not permanent).
        await sync_to_async(
            RunManager.append_event, thread_sensitive=True)(
            run, EVENT_ARTIFACT_DISCOVERED, {
                'artifact_id': str(artifact.pk),
                'external_artifact_id': external_id,
                'provider_artifact_type': provider_type,
                'name': payload.get('name') or '',
            })

    async def _retry_or_fail(self, run: Run, code: str) -> None:
        if run.attempt < run.max_attempts:
            await sync_to_async(
                RunManager.release_interrupted, thread_sensitive=True)(
                run, reason=code)
        else:
            await self._fail(run, code, 'rate limited after retries')

    @staticmethod
    async def _fail(run: Run, code: str, message: str) -> None:
        logger.warning('aily run %s failed: %s %s', run.pk, code, message)
        await sync_to_async(RunManager.finish, thread_sensitive=True)(
            run, Run.Status.FAILED,
            error_code=code, error_message=message)
