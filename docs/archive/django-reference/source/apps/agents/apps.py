"""
App configuration for agents app.
"""
from django.apps import AppConfig


class AgentsConfig(AppConfig):
    """
    Configuration for the agents app.
    """
    default_auto_field = 'django.db.models.BigAutoField'
    name = 'apps.agents'
    verbose_name = 'AI Agents'

    def ready(self):
        """
        Import signal handlers when the app is ready.
        """
        # Import signal handlers here when needed
        # Example: import apps.agents.signals
        pass
