"""Authentication backend for Creation Agent Studio session cookies."""
from rest_framework import authentication, exceptions

from apps.identity.services import load_session


class StudioSessionAuthentication(authentication.BaseAuthentication):
    """Authenticate the signed httpOnly `studio_session` cookie.

    The Studio session proves login to Creation Agent Studio. It is not a
    Feishu UAT and can never be forwarded to Aily.
    """

    def authenticate(self, request):
        token = request.COOKIES.get('studio_session', '')
        if not token:
            return None
        user_id = load_session(token)
        if not user_id:
            raise exceptions.AuthenticationFailed('invalid studio session')

        from django.contrib.auth import get_user_model
        user = get_user_model().objects.filter(pk=user_id, is_active=True).first()
        if user is None:
            raise exceptions.AuthenticationFailed('studio session user not found')
        return user, None
