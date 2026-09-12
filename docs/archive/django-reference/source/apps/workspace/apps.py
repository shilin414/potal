"""
App configuration for workspace app (home workspace / recent / favorites).
"""
from django.apps import AppConfig


class WorkspaceConfig(AppConfig):
    default_auto_field = 'django.db.models.BigAutoField'
    name = 'apps.workspace'
    verbose_name = 'Workspace'
