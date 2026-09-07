"""ASGI config — HTTP via Django, websockets via Channels."""
import os

import django
from channels.routing import ProtocolTypeRouter, URLRouter
from channels.security.websocket import AllowedHostsOriginValidator
from django.core.asgi import get_asgi_application

os.environ.setdefault('DJANGO_SETTINGS_MODULE', 'backend.settings.development')
django.setup()

from apps.app_runner.middleware import JwtWsAuthMiddleware
from apps.app_runner.routing import websocket_urlpatterns

application = ProtocolTypeRouter({
    'http': get_asgi_application(),
    'websocket': AllowedHostsOriginValidator(
        JwtWsAuthMiddleware(URLRouter(websocket_urlpatterns))
    ),
})
