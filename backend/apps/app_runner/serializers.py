"""DRF serializers for app_runner jobs and folder scanning."""
from rest_framework import serializers

from .models import Job


class JobSerializer(serializers.ModelSerializer):
    class Meta:
        model = Job
        fields = ['id', 'app_slug', 'status', 'config', 'error', 'attempt',
                  'max_attempts', 'heartbeat_at', 'created_at', 'finished_at']
        read_only_fields = ['id', 'status', 'error', 'created_at', 'finished_at']


class PublicApplicationSlugField(serializers.SlugRelatedField):
    """SlugRelatedField whose queryset is the public Applications.

    DRF 3.14 asserts a non-null ``queryset`` (or a ``get_queryset`` override
    on the field) at field-init time. We supply it lazily via ``get_queryset``
    so importing the serializer never touches the DB and stays test-safe.
    """

    def get_queryset(self):
        from apps.applications.models import Application
        return Application.objects.filter(is_public=True)


class CreateJobSerializer(serializers.Serializer):
    app_slug = PublicApplicationSlugField(
        slug_field='slug', read_only=False, required=True)
    config = serializers.JSONField(required=True)


class ScanFolderSerializer(serializers.Serializer):
    path = serializers.CharField(required=True, max_length=1024)
