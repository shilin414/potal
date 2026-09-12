"""
App configuration for templates app.
"""
from django.apps import AppConfig


class TemplatesConfig(AppConfig):
    """
    Configuration for the templates app.
    """
    default_auto_field = 'django.db.models.BigAutoField'
    name = 'apps.templates'
    verbose_name = 'Templates'

    def ready(self):
        """
        Import signal handlers when the app is ready.
        """
        # Import signal handlers here when needed
        # Example: import apps.templates.signals
        pass
