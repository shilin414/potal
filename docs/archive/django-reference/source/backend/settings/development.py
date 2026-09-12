"""
Development settings for Creation Agent Studio backend.

Credentials come from backend/.env (gitignored). The database defaults to
TiDB (via base.py DB_TYPE); set DB_TYPE=sqlite in .env for an offline loop.
"""
from .base import *  # noqa: F401,F403

DEBUG = True

# API quotas protect deployed environments, but make local UI development
# brittle because hot reloads and React StrictMode can issue extra requests.
# Set the environment variable to True when throttling needs local testing.
API_RATE_THROTTLING_ENABLED = config(
    'API_RATE_THROTTLING_ENABLED', default=False, cast=bool)
if not API_RATE_THROTTLING_ENABLED:
    REST_FRAMEWORK = {
        **REST_FRAMEWORK,
        'DEFAULT_THROTTLE_CLASSES': [],
    }

# Show Django Debug Toolbar
INSTALLED_APPS = INSTALLED_APPS + ['debug_toolbar']
MIDDLEWARE = MIDDLEWARE + ['debug_toolbar.middleware.DebugToolbarMiddleware']

INTERNAL_IPS = [
    '127.0.0.1',
    'localhost',
]

# More verbose logging
LOGGING['root']['level'] = 'INFO'

# Email backend for development
EMAIL_BACKEND = 'django.core.mail.backends.console.EmailBackend'
