"""
App configuration for identity app (Feishu SSO / admin auth / sessions).
"""
from django.apps import AppConfig


class IdentityConfig(AppConfig):
    default_auto_field = 'django.db.models.BigAutoField'
    name = 'apps.identity'
    verbose_name = 'Identity & Access'
