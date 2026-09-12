"""
Aily provider rate limiting (10 chats/sec per official docs).

Sliding-window limiter stored in Redis with the project key prefix.
"""
from __future__ import annotations

import time

from django.conf import settings

CHATS_PER_SECOND = 10  # POST /agents/:agent_id/chats 官方限制
# "Get chat result" is documented as special rate control. Keep a separate
# conservative bucket so frontend reconnects cannot amplify polling.
POLLS_PER_SECOND = 10
ARTIFACTS_PER_SECOND = 50  # artifact download 官方限制


class RateLimitExceeded(Exception):
    """Raised when the provider start-rate window is exhausted."""

    def __init__(self, retry_after: float):
        super().__init__(f'rate limited, retry after {retry_after:.2f}s')
        self.retry_after = retry_after


class SlidingWindowRateLimiter:
    """Fixed-1s-window counter per official per-second semantics."""

    def __init__(self, redis, limit: int, scope: str):
        self._redis = redis
        self.limit = limit
        self.scope = scope

    def _key(self) -> str:
        window = int(time.time())
        return f'{settings.REDIS_KEY_PREFIX}:rate_limit:{self.scope}:{window}'

    def acquire(self) -> None:
        """Raise RateLimitExceeded if this second is exhausted."""
        key = self._key()
        current = self._redis.incr(key)
        if current == 1:
            self._redis.expire(key, 2)
        if current > self.limit:
            raise RateLimitExceeded(
                retry_after=1.0 - (time.time() % 1.0))

    async def acquire_async(self) -> None:
        import asyncio
        while True:
            try:
                await asyncio.to_thread(self.acquire)
                return
            except RateLimitExceeded as exc:
                await asyncio.sleep(max(exc.retry_after, 0.01))


def chats_limiter(redis) -> SlidingWindowRateLimiter:
    return SlidingWindowRateLimiter(redis, CHATS_PER_SECOND, 'aily:chats')


def polls_limiter(redis) -> SlidingWindowRateLimiter:
    return SlidingWindowRateLimiter(redis, POLLS_PER_SECOND, 'aily:polls')


def artifacts_limiter(redis) -> SlidingWindowRateLimiter:
    return SlidingWindowRateLimiter(redis, ARTIFACTS_PER_SECOND, 'aily:artifacts')
