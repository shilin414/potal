"""
App configuration for execution app (Run / RunEvent / RunLease / Artifact).
"""
from django.apps import AppConfig


class ExecutionConfig(AppConfig):
    default_auto_field = 'django.db.models.BigAutoField'
    name = 'apps.execution'
    verbose_name = 'Execution Plane'
