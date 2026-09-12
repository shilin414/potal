"""
App configuration for projects app.
"""
from django.apps import AppConfig


class ProjectsConfig(AppConfig):
    """
    Configuration for the projects app.
    """
    default_auto_field = 'django.db.models.BigAutoField'
    name = 'apps.projects'
    verbose_name = 'Projects'

    def ready(self):
        """
        Import signal handlers when the app is ready.
        """
        # Import signal handlers here when needed
        # Example: import apps.projects.signals
        pass
