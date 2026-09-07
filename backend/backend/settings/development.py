"""
Development settings for Creation Agent Studio backend.
"""
from .base import *

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

# Database (use SQLite for quick development if needed)
DATABASES = {
    'default': {
        'ENGINE': 'django.db.backends.sqlite3',
        'NAME': BASE_DIR / 'db.sqlite3',
    }
}

# Development and tests are self-contained; production uses Redis from base.py.
CACHES = {
    'default': {
        'BACKEND': 'django.core.cache.backends.locmem.LocMemCache',
        'LOCATION': 'creation-agent-studio-development',
    }
}

# More verbose logging
LOGGING['root']['level'] = 'DEBUG'

# Email backend for development
EMAIL_BACKEND = 'django.core.mail.backends.console.EmailBackend'

# Disable CSRF for API development (use with caution)
# CSRF_COOKIE_SECURE = False
# CSRF_SESSION_HTTPONLY = False
