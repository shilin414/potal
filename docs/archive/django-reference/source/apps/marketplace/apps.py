"""
App configuration for marketplace app.
"""
from django.apps import AppConfig


class MarketplaceConfig(AppConfig):
    """
    Configuration for the marketplace app.
    """
    default_auto_field = 'django.db.models.BigAutoField'
    name = 'apps.marketplace'
    verbose_name = 'Marketplace'

    def ready(self):
        """
        Import signal handlers when the app is ready.
        """
        # Import signal handlers here when needed
        # Example: import apps.marketplace.signals
        pass
