"""
Redis pub/sub fanout for run events (SSE notification plane).

Redis is used only for realtime fanout — TiDB stays the source of truth.
All keys are prefixed with settings.REDIS_KEY_PREFIX (xiaoan3).
"""
from __future__ import annotations

import json
import logging

from django.conf import settings

logger = logging.getLogger(__name__)


def run_channel(run_id) -> str:
    return f'{settings.REDIS_KEY_PREFIX}:run:{run_id}:events'


def user_channel(user_id) -> str:
    return f'{settings.REDIS_KEY_PREFIX}:user:{user_id}:events'


def _get_redis():
    from django_redis import get_redis_connection
    return get_redis_connection('default')


def publish_run_event(run_id, event) -> None:
    """Fan a persisted RunEvent out to Redis subscribers.

    Best-effort: SSE clients also reconcile from TiDB on reconnect, so a
    failed publish degrades latency, not correctness.
    """
    try:
        redis = _get_redis()
    except Exception:  # pragma: no cover - redis down
        logger.warning('run event publish skipped: redis unavailable')
        return
    message = json.dumps({
        'run_id': str(run_id),
        'sequence': event.sequence,
        'event_type': event.event_type,
        'payload': event.payload,
        'created_at': event.created_at.isoformat(),
    }, ensure_ascii=False, default=str)
    try:
        redis.publish(run_channel(run_id), message)
    except Exception:  # pragma: no cover
        logger.warning('run event publish failed for %s', run_id)
