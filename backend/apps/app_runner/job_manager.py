"""In-process job execution + websocket event brokering (single-process)."""
import logging
import threading
import time
from collections import deque
from datetime import datetime, timezone

from asgiref.sync import async_to_sync
from channels.layers import get_channel_layer

from .events import (
    EVT_ITEM_STATE, EVT_JOB_PROGRESS, EVT_JOB_STATE, EVT_LOG, EventSink,
)
from .executors import EXECUTORS  # module-level so tests can monkeypatch jm_mod.EXECUTORS

LOG_RING = 500  # max buffered log events per job (for reconnect replay)

logger = logging.getLogger(__name__)


def _broadcast(job_id: str, event: dict) -> None:
    """Send an event to every subscriber of the job's channel group.

    Broadcasting is best-effort: if no channel layer is configured or the
    backing store (e.g. Redis) is unreachable, the event is simply dropped
    rather than crashing the worker thread. The JobHandle still records it
    locally for snapshot/reconnect replay.
    """
    layer = get_channel_layer()
    if layer is None:
        return
    try:
        async_to_sync(layer.group_send)(
            f'job-{job_id}',
            {'type': 'job.event', 'event': {**event, 'ts': time.time()}},
        )
    except Exception:  # noqa: BLE001 - best-effort fan-out
        pass


class JobHandle:
    """Per-job accumulated state + log ring buffer + cancellation flag."""

    def __init__(self, job_id: str, persist: bool = False):
        self.job_id = job_id
        self._cancel = threading.Event()
        self._lock = threading.Lock()
        self.status = 'pending'
        self.progress = {'current': 0, 'total': 0, 'label': ''}
        self.items: dict[str, dict] = {}
        self.logs: deque = deque(maxlen=LOG_RING)
        self.persist = persist
        self.sequence = 0
        if persist:
            try:
                from django.db.models import Max
                from .models import JobEvent
                self.sequence = JobEvent.objects.filter(job_id=job_id).aggregate(
                    value=Max('sequence'))['value'] or 0
            except Exception:
                logger.exception('Failed to restore event sequence for job %s', job_id)

    @property
    def is_cancelled(self) -> bool:
        return self._cancel.is_set()

    def cancel(self) -> None:
        self._cancel.set()

    def record(self, event: dict) -> None:
        with self._lock:
            t = event.get('type')
            if t == EVT_JOB_STATE:
                self.status = event.get('status', self.status)
            elif t == EVT_JOB_PROGRESS:
                self.progress = {
                    'current': event.get('current', 0),
                    'total': event.get('total', 0),
                    'label': event.get('label', ''),
                }
            elif t == EVT_ITEM_STATE:
                self.items[event['id']] = {
                    'id': event['id'], 'name': event.get('name', ''),
                    'status': event.get('status'), 'result': event.get('result', ''),
                    'error': event.get('error', ''),
                }
            elif t == EVT_LOG:
                self.logs.append({'level': event.get('level', 'info'),
                                  'msg': event.get('msg', '')})
            self.sequence += 1
            sequence = self.sequence
        if self.persist:
            try:
                from .models import Job, JobEvent
                JobEvent.objects.create(
                    job_id=self.job_id, sequence=sequence,
                    event_type=str(event.get('type') or ''), payload=event,
                )
                updates = {'heartbeat_at': datetime.now(timezone.utc)}
                if event.get('type') == EVT_JOB_STATE:
                    updates['status'] = event.get('status', self.status)
                Job.objects.filter(id=self.job_id).update(**updates)
            except Exception:
                logger.exception('Failed to persist event for job %s', self.job_id)
        _broadcast(self.job_id, event)

    def snapshot(self) -> dict:
        with self._lock:
            return {
                'status': self.status,
                'progress': dict(self.progress),
                'items': list(self.items.values()),
                'logs': list(self.logs),
            }


class ChannelsSink:
    """EventSink adapter that records into a JobHandle (and so broadcasts)."""

    def __init__(self, handle: JobHandle):
        self._handle = handle

    def emit(self, type: str, payload: dict) -> None:
        self._handle.record({'type': type, **payload})

    @property
    def cancelled(self) -> bool:
        return self._handle.is_cancelled


class JobManager:
    """Process-wide singleton owning worker threads (single-process deployment)."""

    def __init__(self):
        self._handles: dict[str, JobHandle] = {}
        self._threads: dict[str, threading.Thread] = {}
        self._lock = threading.Lock()

    def _reset(self) -> None:
        """Test helper: clear in-memory state."""
        with self._lock:
            self._handles.clear()
            self._threads.clear()

    def _join_all(self, timeout: float = 5.0) -> None:
        """Test helper: wait for all threads to finish."""
        for t in list(self._threads.values()):
            t.join(timeout=timeout)

    def get_handle(self, job_id: str) -> JobHandle | None:
        return self._handles.get(str(job_id))

    def start(self, job) -> JobHandle:
        job_id = str(job.id)
        executor = EXECUTORS.get(job.app_slug)
        handle = JobHandle(job_id, persist=hasattr(job, '_meta'))
        with self._lock:
            self._handles[job_id] = handle

        if executor is None:
            handle.record({'type': EVT_JOB_STATE, 'status': 'error'})
            handle.record({'type': EVT_LOG, 'level': 'error',
                           'msg': f"未知应用: {job.app_slug}"})
            return handle

        def _run():
            sink = ChannelsSink(handle)
            handle.record({'type': EVT_JOB_STATE, 'status': 'running'})
            if hasattr(job, '_meta'):
                from .models import Job
                Job.objects.filter(id=job.id).update(
                    attempt=getattr(job, 'attempt', 0) + 1,
                    heartbeat_at=datetime.now(timezone.utc),
                )
            try:
                executor.run(job, sink)
                if handle.status not in ('done', 'error', 'stopped'):
                    handle.record({'type': EVT_JOB_STATE, 'status': 'done'})
            except Exception as e:  # noqa: BLE001
                if handle.is_cancelled:
                    handle.record({'type': EVT_JOB_STATE, 'status': 'stopped'})
                else:
                    handle.record({'type': EVT_LOG, 'level': 'error', 'msg': f"执行异常: {e}"})
                    handle.record({'type': EVT_JOB_STATE, 'status': 'error'})
            finally:
                self._mark_finished(job, handle)

        thread = threading.Thread(target=_run, name=f'job-{job_id}', daemon=True)
        with self._lock:
            self._threads[job_id] = thread
        thread.start()
        return handle

    def stop(self, job_id: str) -> bool:
        handle = self._handles.get(str(job_id))
        if handle is None:
            return False
        handle.cancel()
        return True

    def _mark_finished(self, job, handle: JobHandle) -> None:
        try:
            from .models import Job
            Job.objects.filter(id=job.id).update(
                status=handle.status,
                finished_at=datetime.now(timezone.utc),
            )
        except Exception:  # noqa: BLE001 - best-effort persistence
            logger.exception('Failed to persist finished state for job %s', job.id)


# Process-wide singleton.
job_manager = JobManager()
