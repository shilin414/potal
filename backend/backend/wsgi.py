"""
WSGI config for Creation Agent Studio backend.
"""
import os
from django.core.wsgi import get_wsgi_application

os.environ.setdefault('DJANGO_SETTINGS_MODULE', 'backend.settings.base')

application = get_wsgi_application()
