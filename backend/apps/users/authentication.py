"""Authentication backends for scoped API keys."""
from django.utils import timezone
from rest_framework import authentication, exceptions
from rest_framework.permissions import SAFE_METHODS

from .models import UserAPIKey


class ScopedAPIKeyAuthentication(authentication.BaseAuthentication):
    keyword = 'ApiKey'

    def authenticate(self, request):
        raw_key = request.headers.get('X-API-Key', '').strip()
        if not raw_key:
            authorization = authentication.get_authorization_header(request).decode('utf-8')
            if authorization.startswith(f'{self.keyword} '):
                raw_key = authorization[len(self.keyword) + 1:].strip()
        if not raw_key:
            return None

        prefix = raw_key[:12]
        candidates = UserAPIKey.objects.select_related('user').filter(
            prefix=prefix, revoked_at__isnull=True, user__is_active=True
        )
        now = timezone.now()
        for api_key in candidates:
            if api_key.expires_at and api_key.expires_at <= now:
                continue
            if api_key.matches(raw_key):
                required_scope = 'read' if request.method in SAFE_METHODS else 'write'
                scopes = set(api_key.scopes or [])
                if scopes and not scopes.intersection(
                        {required_scope, 'admin', '*'}):
                    raise exceptions.PermissionDenied(
                        f'API key does not include the {required_scope!r} scope.')
                UserAPIKey.objects.filter(pk=api_key.pk).update(last_used_at=now)
                request.api_key = api_key
                return api_key.user, api_key
        raise exceptions.AuthenticationFailed('Invalid or expired API key.')
