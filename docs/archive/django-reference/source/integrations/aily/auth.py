"""
Provider auth resolution for Aily calls.

Studio authentication (login session) is fully separated from provider
credentials (UAT/TAT). This module resolves a ProviderAuthContext at call
time; tokens live in Redis with a TTL, never in the database.
"""
from __future__ import annotations

import time

import httpx
from asgiref.sync import sync_to_async
from django.conf import settings

from apps.catalog.runtime import ProviderAuthContext

PROVIDER_KEY = 'feishu_aily'

# Feishu token endpoints (open platform standard OAuth).
TENANT_TOKEN_PATH = '/open-apis/auth/v3/tenant_access_token/internal'
USER_TOKEN_PATH = '/open-apis/authen/v2/oauth/token'


class FeishuTokenCache:
    """Redis-backed token cache with the project key prefix.

    Values: {'token': ..., 'expires_at': epoch_seconds}.
    """

    def __init__(self, redis=None):
        self._redis = redis

    def _conn(self):
        if self._redis is None:
            from django_redis import get_redis_connection
            self._redis = get_redis_connection('default')
        return self._redis

    @staticmethod
    def _key(kind: str, ident: str) -> str:
        return f'{settings.REDIS_KEY_PREFIX}:provider:aily:{kind}:{ident}'

    def get(self, kind: str, ident: str) -> str | None:
        raw = self._conn().get(self._key(kind, ident))
        if not raw:
            return None
        import json
        record = json.loads(raw)
        if record.get('expires_at', 0) - time.time() < 30:
            return None
        return record.get('token')

    def set(self, kind: str, ident: str, token: str, ttl: int) -> None:
        import json
        self._conn().set(
            self._key(kind, ident),
            json.dumps({'token': token, 'expires_at': time.time() + ttl}),
            ex=max(ttl, 60),
        )


_token_cache = FeishuTokenCache()


async def _post_token(path: str, payload: dict) -> dict:
    url = settings.FEISHU_BASE_URL.rstrip('/') + path
    async with httpx.AsyncClient(timeout=15) as client:
        response = await client.post(url, json=payload)
        response.raise_for_status()
        result = response.json()
    if result.get('code') not in (None, 0):
        raise LookupError(result.get('msg') or 'feishu token request failed')
    data = result.get('data')
    return data if isinstance(data, dict) else result


async def _request_user_token(refresh_token: str) -> dict:
    return await _post_token(USER_TOKEN_PATH, {
        'grant_type': 'refresh_token',
        'client_id': settings.FEISHU_APP_ID,
        'client_secret': settings.FEISHU_APP_SECRET,
        'refresh_token': refresh_token,
    })


async def _request_tenant_token() -> dict:
    return await _post_token(TENANT_TOKEN_PATH, {
        'app_id': settings.FEISHU_APP_ID,
        'app_secret': settings.FEISHU_APP_SECRET,
    })


async def get_user_access_token(user) -> str:
    """Resolve (and cache) a UAT for the logged-in Feishu user.

    Requires a stored refresh token issued during the Feishu OAuth login
    flow (apps.identity). Raises if the user has no Feishu identity.
    """
    ident = _user_ident(user)
    cached = await sync_to_async(_token_cache.get)('uat', ident)
    if cached:
        return cached

    from apps.identity.models import FeishuIdentity
    identity = await FeishuIdentity.objects.filter(user=user).afirst()
    if identity is None or not identity.refresh_token:
        raise LookupError(
            'user has no feishu identity; re-login through Feishu OAuth')

    refresh_token = await sync_to_async(identity.decrypted_refresh_token)()
    result = await _request_user_token(refresh_token)
    token = result.get('access_token') or result.get('user_access_token')
    if not token:
        raise LookupError('feishu token endpoint returned no access_token')
    ttl = int(result.get('expires_in') or 7200)
    new_refresh = result.get('refresh_token')
    if new_refresh:
        await identity.rotate_refresh_token(new_refresh)
    await sync_to_async(_token_cache.set)('uat', ident, token, ttl)
    return token


async def get_tenant_access_token() -> str:
    """Resolve (and cache) the app TAT — only for tenant-mode bindings."""
    ident = settings.FEISHU_APP_ID
    cached = await sync_to_async(_token_cache.get)('tat', ident)
    if cached:
        return cached
    result = await _request_tenant_token()
    token = result.get('tenant_access_token')
    if not token:
        raise LookupError('feishu token endpoint returned no tenant_access_token')
    ttl = int(result.get('expire') or 7000)
    await sync_to_async(_token_cache.set)('tat', ident, token, ttl)
    return token


def _user_ident(user) -> str:
    feishu_open_id = getattr(user, 'feishu_open_id', '') or ''
    return feishu_open_id or f'user:{getattr(user, "pk", "")}'


async def build_auth_context(user, identity_mode: str = 'user') -> ProviderAuthContext:
    """Build the ProviderAuthContext for an Aily call.

    identity_mode 'user' (default, per project requirement) resolves a UAT
    for the calling user; 'tenant' resolves the app TAT.
    """
    if identity_mode == 'user':
        token = await get_user_access_token(user)
        return ProviderAuthContext(
            provider=PROVIDER_KEY,
            identity_mode='user',
            subject_user_id=str(getattr(user, 'pk', '')),
            tenant_id=settings.FEISHU_APP_ID,
            credential_ref=f'feishu_uat:{_user_ident(user)}',
            token=token,
        )
    token = await get_tenant_access_token()
    return ProviderAuthContext(
        provider=PROVIDER_KEY,
        identity_mode='tenant',
        tenant_id=settings.FEISHU_APP_ID,
        credential_ref=f'feishu_tat:{settings.FEISHU_APP_ID}',
        token=token,
    )
