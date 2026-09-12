"""
App configuration for applications app.
"""
from django.apps import AppConfig


class ApplicationsConfig(AppConfig):
    """
    Configuration for the applications (App Center) app.
    """
    default_auto_field = 'django.db.models.BigAutoField'
    name = 'apps.applications'
    verbose_name = 'Applications (App Center)'

    def ready(self):
        """
        Import signal handlers when the app is ready.
        """
        pass
