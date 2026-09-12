"""
App configuration for catalog app (Application / Provider / RuntimeBinding).
"""
from django.apps import AppConfig


class CatalogConfig(AppConfig):
    default_auto_field = 'django.db.models.BigAutoField'
    name = 'apps.catalog'
    verbose_name = 'Application Catalog'

    def ready(self):
        # Register built-in runtime adapters with the RuntimeRegistry.
        from integrations.aily import register
        register()
