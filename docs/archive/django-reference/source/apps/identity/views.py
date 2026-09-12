"""Identity API: Feishu OAuth (default user entry) and admin login."""
from __future__ import annotations

import logging

from asgiref.sync import sync_to_async
from django.conf import settings
from django.contrib.auth import authenticate, get_user_model, login
from django.http import JsonResponse
from django.urls import path
from django.views.decorators.csrf import csrf_exempt

from apps.users.display import session_user_payload

logger = logging.getLogger(__name__)
User = get_user_model()


def _json_error(message: str, status: int = 400):
    return JsonResponse({'error': message}, status=status)


# ---------------------------------------------------------------------------
# Feishu OAuth (normal-user default entry)
# ---------------------------------------------------------------------------

def oauth_start(request):
    """GET /api/identity/oauth/start

    Redirect to Feishu OAuth. This is the only login entry normal users
    ever see: no username/password page, no login-method chooser.
    """
    from apps.identity.services import build_authorize_url, make_state
    from django.shortcuts import redirect

    redirect_uri = settings.FEISHU_REDIRECT_URI
    if not redirect_uri:
        return _json_error('FEISHU_REDIRECT_URI is not configured', 500)
    return_to = request.GET.get('return_to', '/')
    if not return_to.startswith('/') or return_to.startswith('//'):
        return_to = '/'
    return redirect(build_authorize_url(make_state(return_to), redirect_uri))


async def oauth_callback(request):
    """GET /api/identity/oauth/callback?code=...&state=...

    Frontend callback page calls this exchange endpoint via XHR so the
    studio_session cookie is set on the app origin. Exchanges the code,
    maps the local user, issues the Studio session cookie + JWT.
    """
    from apps.identity.services import (
        SESSION_MAX_AGE,
        exchange_code,
        fetch_user_info,
        load_state,
        sign_session,
        upsert_feishu_user,
    )

    code = request.GET.get('code')
    state = load_state(request.GET.get('state', ''))
    if not code:
        return _json_error('missing code')
    if state is None:
        return _json_error('invalid or expired oauth state')
    redirect_uri = settings.FEISHU_REDIRECT_URI

    try:
        tokens = await exchange_code(code, redirect_uri)
    except Exception as exc:  # noqa: BLE001
        logger.warning('feishu oauth exchange failed: %s', exc)
        return _json_error('oauth exchange failed', 502)

    access_token = tokens.get('access_token') or tokens.get(
        'user_access_token') or ''
    if not access_token:
        return _json_error('oauth returned no access token', 502)

    try:
        user_info = await fetch_user_info(access_token)
    except Exception as exc:  # noqa: BLE001
        logger.warning('feishu user info failed: %s', exc)
        return _json_error('user info failed', 502)

    user, _identity = await sync_to_async(upsert_feishu_user)(user_info, tokens)

    # JWT pair keeps the existing axios interceptor working; the signed
    # cookie authorizes the unified /api/v2 run APIs.
    from rest_framework_simplejwt.tokens import RefreshToken

    def _issue_tokens():
        refresh = RefreshToken.for_user(user)
        return str(refresh.access_token), str(refresh)

    access_token_jwt, refresh_token_jwt = await sync_to_async(
        _issue_tokens, thread_sensitive=True)()

    response = JsonResponse({
        'user': session_user_payload(user),
        'tokens': {
            'access': access_token_jwt,
            'refresh': refresh_token_jwt,
        },
        'return_to': state.get('return_to') or '/',
    })
    response.set_cookie(
        'studio_session', sign_session(user.pk),
        max_age=SESSION_MAX_AGE, httponly=True, samesite='Lax',
        secure=not settings.DEBUG)
    return response


def session_info(request):
    """GET /api/identity/session — current Studio session user."""
    from apps.identity.services import load_session

    token = request.COOKIES.get('studio_session', '')
    uid = load_session(token) if token else None
    if not uid:
        return _json_error('not authenticated', 401)
    user = User.objects.filter(pk=uid).first()
    if user is None:
        return _json_error('not authenticated', 401)
    payload = session_user_payload(user)
    # The session endpoint exposes only the fields a restored UI session needs
    # (no email/role), plus the server-side avatar URL.
    return JsonResponse({
        'id': payload['id'],
        'username': payload['username'],
        'display_name': payload['display_name'],
        'display_id': payload['display_id'],
        'avatar_url': payload['avatar_url'],
        'auth_source': payload['auth_source'],
        'is_staff': payload['is_staff'],
    })


# ---------------------------------------------------------------------------
# Admin login (dedicated entry at /login/admin)
# ---------------------------------------------------------------------------

@csrf_exempt
def admin_login(request):
    """POST /api/identity/admin/login {username, password}

    Local admin authentication only; normal users never see this form.
    """
    if request.method != 'POST':
        return _json_error('POST required', 405)
    import json
    try:
        body = json.loads(request.body or b'{}')
    except (ValueError, json.JSONDecodeError):
        return _json_error('invalid json')
    user = authenticate(
        request,
        username=body.get('username', ''),
        password=body.get('password', ''),
    )
    if user is None or not user.is_staff:
        return _json_error('invalid credentials', 401)
    login(request, user)
    payload = session_user_payload(user)
    return JsonResponse({
        'id': payload['id'],
        'username': payload['username'],
        'is_staff': payload['is_staff'],
        'auth_source': 'local_admin',
        'display_name': payload['display_name'],
        'display_id': payload['display_id'],
    })


urlpatterns = [
    path('oauth/start', oauth_start, name='identity-oauth-start'),
    path('oauth/exchange', oauth_callback, name='identity-oauth-exchange'),
    path('session', session_info, name='identity-session'),
    path('admin/login', admin_login, name='identity-admin-login'),
]
