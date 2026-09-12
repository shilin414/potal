"""
Feishu OAuth login flow for normal users.

Flow (architecture doc §41):
    open / -> no Studio session -> redirect Feishu OAuth
    -> code -> user access token -> feishu user info
    -> map/create local User (auth_source=feishu) -> issue Studio session
"""
from __future__ import annotations

import logging
from typing import Optional

import httpx
from django.conf import settings
from django.contrib.auth import get_user_model
from django.core import signing
from django.utils import timezone

from apps.identity.crypto import encrypt_if_configured
from apps.identity.models import FeishuIdentity

logger = logging.getLogger(__name__)
User = get_user_model()

AUTHORIZE_PATH = '/open-apis/authen/v1/authorize'
TOKEN_PATH = '/open-apis/authen/v2/oauth/token'
USER_INFO_PATH = '/open-apis/authen/v1/user_info'
# Studio session lifetime (seconds).
SESSION_MAX_AGE = 12 * 3600

# Scopes requested at authorize time (official Aily docs, 自定义智能体):
#   offline_access            -> v2 token endpoint issues a refresh_token
#   aily:agent_chat:write     -> 发起对话/会话管理
#   aily:agent_chat:read      -> 获取对话结果/会话查询
#   aily:agent_attachment:write -> 上传附件
#   aily:agent_artifact:read  -> 产物下载
#   aily:agent_visibility:read -> 可见性校验
# Ungranted scopes surface later as 403 code 99991679 on the Aily call.
OAUTH_SCOPES = (
    'offline_access '
    'aily:agent_chat:write aily:agent_chat:read '
    'aily:agent_attachment:write '
    'aily:agent_artifact:read '
    'aily:agent_visibility:read'
)


def _base() -> str:
    return settings.FEISHU_BASE_URL.rstrip('/')


def build_authorize_url(state: str, redirect_uri: str) -> str:
    """Build the Feishu OAuth authorize URL (user identity).

    `offline_access` is required by the v2 OAuth token endpoint to issue a
    refresh_token; without it login still succeeds but the worker can never
    renew a UAT for Aily calls ('user has no feishu identity' on re-run).
    """
    import urllib.parse
    params = {
        'client_id': settings.FEISHU_APP_ID,
        'redirect_uri': redirect_uri,
        'response_type': 'code',
        'state': state,
        'scope': OAUTH_SCOPES,
    }
    return f'{_base()}{AUTHORIZE_PATH}?{urllib.parse.urlencode(params)}'


def make_state(return_to: str = '/') -> str:
    """Create a short-lived signed OAuth state token."""
    return signing.dumps({'return_to': return_to}, salt='feishu-oauth-state')


def load_state(state: str) -> Optional[dict]:
    try:
        return signing.loads(
            state, salt='feishu-oauth-state', max_age=10 * 60)
    except signing.BadSignature:
        return None


def sign_session(user_id) -> str:
    """Create the Studio session token (signed, not a provider token)."""
    return signing.dumps({'uid': str(user_id)}, salt='studio-session')


def load_session(token: str):
    """Return the user id bound to a Studio session token, or None."""
    try:
        data = signing.loads(
            token, salt='studio-session', max_age=SESSION_MAX_AGE)
        return data.get('uid')
    except signing.BadSignature:
        return None


async def exchange_code(code: str, redirect_uri: str) -> dict:
    """Exchange the OAuth code for user tokens (UAT + refresh)."""
    async with httpx.AsyncClient(timeout=20) as client:
        response = await client.post(f'{_base()}{TOKEN_PATH}', json={
            'grant_type': 'authorization_code',
            'client_id': settings.FEISHU_APP_ID,
            'client_secret': settings.FEISHU_APP_SECRET,
            'code': code,
            'redirect_uri': redirect_uri,
        })
        response.raise_for_status()
        result = response.json()
    if result.get('code') not in (None, 0):
        raise LookupError(result.get('msg') or 'feishu oauth exchange failed')
    data = result.get('data')
    return data if isinstance(data, dict) else result


async def fetch_user_info(access_token: str) -> dict:
    async with httpx.AsyncClient(timeout=20) as client:
        response = await client.get(
            f'{_base()}{USER_INFO_PATH}',
            headers={'Authorization': f'Bearer {access_token}'})
        response.raise_for_status()
        return response.json().get('data', {})


def upsert_feishu_user(user_info: dict, tokens: Optional[dict]) -> tuple:
    """Map or create the local User from Feishu identity data.

    Returns (user, identity).
    """
    open_id = user_info.get('open_id') or ''
    # `user_id` (tenant employee id, e.g. 19127920) is only returned when the
    # app holds the employee-id scope; `employee_no` is the fallback key.
    # Either one is what the chat UI shows as 姓名（user_id）.
    feishu_user_id = (
        user_info.get('user_id') or user_info.get('employee_no') or '')
    name = user_info.get('name') or open_id or feishu_user_id or 'feishu_user'

    identity = None
    if open_id:
        identity = FeishuIdentity.objects.filter(open_id=open_id).first()
    if identity is None and feishu_user_id:
        identity = FeishuIdentity.objects.filter(
            feishu_user_id=feishu_user_id).first()

    if identity is not None:
        user = identity.user
    else:
        # Derive a unique username from the Feishu open id.
        base = (user_info.get('en_name') or name or 'feishu_user')
        username = base
        suffix = 1
        while User.objects.filter(username=username).exists():
            username = f'{base}_{suffix}'
            suffix += 1
        user = User.objects.create(
            username=username,
            first_name=name[:30],
            auth_source=User.AuthSource.FEISHU,
        )
        user.set_unusable_password()
        user.save(update_fields=['password'])
        identity = FeishuIdentity(user=user)

    # Refresh profile snapshot.
    identity.open_id = open_id or identity.open_id or None
    identity.feishu_user_id = feishu_user_id or identity.feishu_user_id or None
    identity.union_id = user_info.get('union_id') or identity.union_id
    identity.display_name = name or identity.display_name
    identity.avatar_url = user_info.get('avatar_url') or identity.avatar_url
    if tokens and tokens.get('refresh_token'):
        identity.refresh_token = encrypt_if_configured(tokens['refresh_token'])
        identity.refresh_token_expires_at = timezone.now() + timezone.timedelta(
            seconds=int(tokens.get('refresh_token_expires_in') or 30 * 86400))
    identity.last_login_at = timezone.now()
    identity.save()

    # Mirror display fields onto the unified user. `display_id` (shown as
    # 姓名（user_id） in the chat UI) is only seeded when the profile is still
    # empty: an operator-set value must win over the login snapshot.
    user_updates = {
        'first_name': name[:150],
        'avatar': identity.avatar_url or '',
        'auth_source': User.AuthSource.FEISHU,
    }
    if feishu_user_id and not (getattr(user, 'display_id', '') or ''):
        user_updates['display_id'] = feishu_user_id
    User.objects.filter(pk=user.pk).update(**user_updates)
    return user, identity
