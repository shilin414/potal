"""Validate the SimpleJWT access token passed as ?token= on websockets."""
from urllib.parse import parse_qs

from channels.db import database_sync_to_async
from django.contrib.auth import get_user_model
from rest_framework_simplejwt.exceptions import InvalidToken, TokenError
from rest_framework_simplejwt.tokens import AccessToken


class JwtWsAuthMiddleware:
    """Channels middleware: populates scope['user'] from a ?token= query param."""

    def __init__(self, inner):
        self.inner = inner

    async def __call__(self, scope, receive, send):
        query = parse_qs(scope.get('query_string', b'').decode())
        token = query.get('token', [None])[0]
        scope['user'] = None
        if token:
            scope['user'] = await self._user_from_token(token)
        return await self.inner(scope, receive, send)

    @database_sync_to_async
    def _user_from_token(self, token):
        try:
            access = AccessToken(token)
            return get_user_model().objects.get(id=access['user_id'])
        except (InvalidToken, TokenError, get_user_model().DoesNotExist):
            return None
