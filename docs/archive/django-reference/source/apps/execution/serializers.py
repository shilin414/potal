"""Serializers for the v2 execution API."""
from rest_framework import serializers

from apps.catalog.models import ApplicationRuntimeBinding, Provider
from apps.execution.models import (
    Run,
    RunArtifact,
    RunCommand,
    RunEvent,
    RunLease,
    RuntimeAttachment,
)


class ProviderSerializer(serializers.ModelSerializer):
    class Meta:
        model = Provider
        fields = [
            'id', 'key', 'name', 'description', 'supported_runtime_types',
            'capabilities', 'start_rate_limit', 'max_inflight',
            'timeout_seconds', 'status',
        ]


class RuntimeBindingSerializer(serializers.ModelSerializer):
    provider_name = serializers.CharField(
        source='provider.name', read_only=True, default='')

    class Meta:
        model = ApplicationRuntimeBinding
        fields = [
            'id', 'application', 'runtime_type', 'provider', 'provider_key',
            'provider_name', 'external_resource_id', 'identity_mode',
            'execution_mode', 'session_policy', 'artifact_policy',
            'capabilities', 'timeout_seconds', 'enabled',
        ]


class RunSerializer(serializers.ModelSerializer):
    class Meta:
        model = Run
        fields = [
            'id', 'organization', 'user', 'application', 'conversation',
            'runtime_binding', 'provider', 'runtime_type', 'external_run_id',
            'status', 'provider_status', 'provider_finish_reason',
            'input', 'output', 'attempt', 'max_attempts',
            'queued_at', 'started_at', 'finished_at',
            'error_code', 'error_message', 'created_at', 'updated_at',
        ]
        read_only_fields = fields


class RunEventSerializer(serializers.ModelSerializer):
    class Meta:
        model = RunEvent
        fields = ['id', 'run', 'sequence', 'event_type', 'payload', 'created_at']


class RunCommandSerializer(serializers.ModelSerializer):
    class Meta:
        model = RunCommand
        fields = [
            'id', 'run', 'command_type', 'payload', 'status',
            'created_by', 'created_at', 'resolved_at']
        read_only_fields = ['id', 'run', 'status', 'created_by',
                            'created_at', 'resolved_at']


class RunArtifactSerializer(serializers.ModelSerializer):
    class Meta:
        model = RunArtifact
        fields = [
            'id', 'run', 'provider', 'external_artifact_id',
            'provider_artifact_type', 'name', 'normalized_type',
            'storage_type', 'cached_url_expires_at', 'resolution_status',
            'created_at', 'updated_at']


class RuntimeAttachmentSerializer(serializers.ModelSerializer):
    class Meta:
        model = RuntimeAttachment
        fields = [
            'id', 'run', 'conversation', 'provider',
            'external_attachment_id', 'attachment_type', 'name',
            'source_type', 'status', 'created_at']


class RunLeaseSerializer(serializers.ModelSerializer):
    class Meta:
        model = RunLease
        fields = ['id', 'run', 'worker_id', 'acquired_at',
                  'heartbeat_at', 'expires_at']
