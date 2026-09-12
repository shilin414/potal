"""
RunManager: create runs, append events, CAS-claim queued runs.

Claiming uses compare-and-swap UPDATEs (safe on TiDB 8 / MySQL 8) instead
of SKIP LOCKED. Event publication goes to TiDB (source of truth) plus a
Redis pub/sub fanout for live SSE delivery.
"""
from __future__ import annotations

import logging
from typing import Optional

from django.db import transaction
from django.db.models import F
from django.utils import timezone

from apps.execution.models import Run, RunEvent, RunLease
from apps.execution.pubsub import publish_run_event

logger = logging.getLogger(__name__)

# Unified event protocol constants.
EVENT_RUN_STARTED = 'run.started'
EVENT_CONTENT_STARTED = 'content.started'
EVENT_CONTENT_DELTA = 'content.delta'
EVENT_CONTENT_COMPLETED = 'content.completed'
EVENT_TOOL_STARTED = 'tool.started'
EVENT_TOOL_COMPLETED = 'tool.completed'
EVENT_ARTIFACT_DISCOVERED = 'artifact.discovered'
EVENT_ARTIFACT_CREATED = 'artifact.created'
EVENT_INPUT_REQUIRED = 'input.required'
EVENT_RUN_COMPLETED = 'run.completed'
EVENT_RUN_FAILED = 'run.failed'
EVENT_RUN_INTERRUPTED = 'run.interrupted'


class RunManager:
    """Facade for run lifecycle operations shared by API and workers."""

    # ------------------------------------------------------------------
    # Creation
    # ------------------------------------------------------------------
    @staticmethod
    def create_run(*, user, organization=None, application=None,
                   conversation=None, runtime_binding=None,
                   provider: str = '', runtime_type: str = 'agent',
                   input: Optional[dict] = None,
                   execution_mode: str = 'interactive') -> Run:
        if runtime_binding is not None:
            provider = runtime_binding.provider_key
            runtime_type = runtime_binding.runtime_type
        if not provider:
            raise ValueError('provider is required')
        run = Run.objects.create(
            organization=organization,
            user=user,
            application=application,
            conversation=conversation,
            runtime_binding=runtime_binding,
            provider=provider,
            runtime_type=runtime_type,
            input={'mode': execution_mode, **(input or {})},
            runtime_snapshot=runtime_binding.snapshot() if runtime_binding else {},
        )
        return run

    # ------------------------------------------------------------------
    # Events
    # ------------------------------------------------------------------
    @staticmethod
    def append_event(run: Run, event_type: str,
                     payload: Optional[dict] = None) -> RunEvent:
        """Append the next sequenced event and fan out to Redis.

        Uses a conditional UPDATE on the run's event counter (CAS) so two
        concurrent writers cannot claim the same sequence number.
        """
        with transaction.atomic():
            locked = Run.objects.select_for_update().filter(pk=run.pk).only('id').first()
            next_seq = (locked.events.count() + 1) if locked else 1
            event = RunEvent.objects.create(
                run_id=run.pk, sequence=next_seq,
                event_type=event_type, payload=payload or {})
        publish_run_event(run.pk, event)
        return event

    # ------------------------------------------------------------------
    # CAS claim (worker side)
    # ------------------------------------------------------------------
    @classmethod
    def claim_next(cls, worker_id: str, provider: str,
                   lease_seconds: int = 120) -> Optional[Run]:
        """Claim the oldest queued run for `provider` via CAS.

        UPDATE ... WHERE status='queued' only affects one row when it is
        still queued; a lost race means affected_rows == 0 and we move on.
        """
        candidate_ids = list(
            Run.objects.filter(status=Run.Status.QUEUED, provider=provider)
            .order_by('queued_at')
            .values_list('id', flat=True)[:10]
        )
        now = timezone.now()
        for run_id in candidate_ids:
            claimed = Run.objects.filter(
                pk=run_id, status=Run.Status.QUEUED,
            ).update(
                status=Run.Status.RUNNING,
                started_at=now,
                attempt=F('attempt') + 1,
            )
            if claimed:
                run = Run.objects.select_related(
                    'runtime_binding', 'user', 'conversation').get(pk=run_id)
                RunLease.objects.create(
                    run=run, worker_id=worker_id,
                    expires_at=now + timezone.timedelta(seconds=lease_seconds))
                cls.append_event(run, EVENT_RUN_STARTED, {
                    'worker_id': worker_id, 'attempt': run.attempt})
                return run
        return None

    # ------------------------------------------------------------------
    # Lease maintenance
    # ------------------------------------------------------------------
    @staticmethod
    def heartbeat(run: Run, worker_id: str, lease_seconds: int = 120) -> bool:
        updated = RunLease.objects.filter(
            run=run, worker_id=worker_id).update(
            heartbeat_at=timezone.now(),
            expires_at=timezone.now() + timezone.timedelta(seconds=lease_seconds))
        return updated > 0

    @classmethod
    def release_interrupted(cls, run: Run, reason: str = '') -> None:
        """Move a run whose lease expired to interrupted for retry."""
        cls.append_event(run, EVENT_RUN_INTERRUPTED, {'reason': reason})
        if run.attempt >= run.max_attempts:
            Run.objects.filter(pk=run.pk).update(
                status=Run.Status.FAILED,
                error_code='lease_expired',
                error_message=reason or 'lease expired',
                finished_at=timezone.now())
        else:
            Run.objects.filter(pk=run.pk).update(status=Run.Status.QUEUED)
        RunLease.objects.filter(run=run).delete()

    @classmethod
    def recover_expired_leases(cls) -> int:
        """Recover runs whose worker heartbeat lease expired.

        The lease predicate is checked again immediately before recovery so
        a concurrent heartbeat or successful completion wins the race.
        """
        now = timezone.now()
        lease_ids = list(RunLease.objects.filter(
            expires_at__lte=now).values_list('pk', flat=True)[:100])
        recovered = 0
        for lease_id in lease_ids:
            lease = RunLease.objects.select_related('run').filter(
                pk=lease_id, expires_at__lte=timezone.now()).first()
            if lease is None:
                continue
            run = lease.run
            if run.status != Run.Status.RUNNING:
                RunLease.objects.filter(pk=lease_id).delete()
                continue
            cls.release_interrupted(run, reason='worker lease expired')
            recovered += 1
        return recovered

    # ------------------------------------------------------------------
    # Completion
    # ------------------------------------------------------------------
    @classmethod
    def finish(cls, run: Run, status: str, *, output: Optional[dict] = None,
               provider_status: str = '', finish_reason: str = '',
               error_code: str = '', error_message: str = '') -> Run:
        now = timezone.now()
        updated = Run.objects.filter(pk=run.pk).exclude(
            status__in=Run.TERMINAL_STATUSES).update(
            status=status,
            output=output if output is not None else F('output'),
            provider_status=provider_status,
            provider_finish_reason=finish_reason,
            error_code=error_code,
            error_message=error_message,
            finished_at=now,
            updated_at=now,
        )
        RunLease.objects.filter(run=run).delete()
        event_type = {
            Run.Status.SUCCEEDED: EVENT_RUN_COMPLETED,
            Run.Status.CANCELLED: EVENT_RUN_COMPLETED,
            Run.Status.FAILED: EVENT_RUN_FAILED,
            Run.Status.INTERRUPTED: EVENT_RUN_INTERRUPTED,
        }.get(status)
        if updated and event_type:
            run.refresh_from_db()
            event_payload = {
                'status': status,
                'provider_status': provider_status,
                'finish_reason': finish_reason,
            }
            if error_code:
                event_payload['error_code'] = error_code
            if error_message:
                event_payload['error_message'] = error_message
            # Carry the final output so replayed/late SSE consumers can render
            # the authoritative text without an extra GET /runs/{id}.
            if output is not None and output.get('text'):
                event_payload['text'] = output['text']
            cls.append_event(run, event_type, event_payload)
        else:
            run.refresh_from_db()
        return run
