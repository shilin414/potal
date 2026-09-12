"""
Execution models: unified Run, RunEvent, RunCommand, RunArtifact,
RuntimeAttachment, RunLease.

Every real execution — Aily agent, Aily workflow, Codex, GraphFlow, HTTP,
media — is one Run. Internal status is platform-defined; provider raw
status is kept separately on Run.provider_status.
"""
import uuid

from django.conf import settings
from django.db import models


class Run(models.Model):
    """A single unified execution of an Application."""

    class Status(models.TextChoices):
        QUEUED = 'queued', 'Queued'
        RUNNING = 'running', 'Running'
        WAITING_INPUT = 'waiting_input', 'Waiting input'
        WAITING_EXTERNAL = 'waiting_external', 'Waiting external'
        CANCELLING = 'cancelling', 'Cancelling'
        CANCELLED = 'cancelled', 'Cancelled'
        SUCCEEDED = 'succeeded', 'Succeeded'
        FAILED = 'failed', 'Failed'
        INTERRUPTED = 'interrupted', 'Interrupted'

    TERMINAL_STATUSES = frozenset({
        Status.CANCELLED, Status.SUCCEEDED, Status.FAILED,
    })

    id = models.UUIDField(primary_key=True, default=uuid.uuid4, editable=False)
    organization = models.ForeignKey(
        'enterprise.Organization', on_delete=models.SET_NULL,
        null=True, blank=True, related_name='runs')
    user = models.ForeignKey(
        settings.AUTH_USER_MODEL, on_delete=models.SET_NULL,
        null=True, blank=True, related_name='runs')
    application = models.ForeignKey(
        'applications.Application', on_delete=models.PROTECT,
        null=True, blank=True, related_name='runs')
    conversation = models.ForeignKey(
        'conversations.Conversation', on_delete=models.CASCADE,
        null=True, blank=True, related_name='runs')
    workflow_run = models.ForeignKey(
        'workflows.WorkflowRun', on_delete=models.SET_NULL,
        null=True, blank=True, related_name='runs')
    runtime_binding = models.ForeignKey(
        'catalog.ApplicationRuntimeBinding', on_delete=models.PROTECT,
        null=True, blank=True, related_name='runs')

    provider = models.CharField(max_length=64, db_index=True)
    runtime_type = models.CharField(max_length=20, db_index=True)
    # Aily: agent_chat_id.
    external_run_id = models.CharField(max_length=255, blank=True, default='')

    status = models.CharField(
        max_length=20, choices=Status.choices, default=Status.QUEUED,
        db_index=True)
    provider_status = models.CharField(max_length=64, blank=True, default='')
    provider_finish_reason = models.CharField(max_length=64, blank=True, default='')

    input = models.JSONField(default=dict, blank=True)
    output = models.JSONField(default=dict, blank=True)
    # Binding snapshot at submit time; never contains tokens.
    runtime_snapshot = models.JSONField(default=dict, blank=True)

    attempt = models.PositiveIntegerField(default=0)
    max_attempts = models.PositiveIntegerField(default=3)

    queued_at = models.DateTimeField(auto_now_add=True)
    started_at = models.DateTimeField(null=True, blank=True)
    finished_at = models.DateTimeField(null=True, blank=True)

    error_code = models.CharField(max_length=64, blank=True, default='')
    error_message = models.TextField(blank=True, default='')

    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        db_table = 'execution_runs'
        ordering = ['-created_at']
        indexes = [
            # CAS claim scan: queued runs ordered oldest-first.
            models.Index(fields=['status', 'provider', 'queued_at']),
            models.Index(fields=['conversation', 'created_at']),
            models.Index(fields=['user', 'created_at']),
        ]

    def __str__(self):
        return f'Run<{self.provider}:{self.runtime_type}:{self.status}>'

    @property
    def is_terminal(self) -> bool:
        return self.status in self.TERMINAL_STATUSES


class RunEvent(models.Model):
    """Durable ordered event stream for a Run.

    Unified event protocol: run.started, content.started, content.delta,
    content.completed, tool.started, tool.completed, artifact.discovered,
    artifact.created, input.required, run.completed, run.failed,
    run.interrupted.
    """

    id = models.BigAutoField(primary_key=True)
    run = models.ForeignKey(Run, on_delete=models.CASCADE, related_name='events')
    sequence = models.PositiveBigIntegerField()
    event_type = models.CharField(max_length=64, db_index=True)
    payload = models.JSONField(default=dict, blank=True)
    created_at = models.DateTimeField(auto_now_add=True, db_index=True)

    class Meta:
        db_table = 'execution_run_events'
        ordering = ['sequence']
        constraints = [models.UniqueConstraint(
            fields=['run', 'sequence'], name='unique_run_event_sequence')]

    def __str__(self):
        return f'{self.run_id}#{self.sequence}:{self.event_type}'


class RunCommand(models.Model):
    """User-issued command toward a Run (cancel, respond, resume...)."""

    class Type(models.TextChoices):
        CANCEL = 'cancel', 'Cancel'
        RESPOND = 'respond', 'Respond (human input)'
        RESUME = 'resume', 'Resume'

    class Status(models.TextChoices):
        PENDING = 'pending', 'Pending'
        ACCEPTED = 'accepted', 'Accepted'
        REJECTED = 'rejected', 'Rejected'

    id = models.UUIDField(primary_key=True, default=uuid.uuid4, editable=False)
    run = models.ForeignKey(Run, on_delete=models.CASCADE, related_name='commands')
    command_type = models.CharField(max_length=20, choices=Type.choices)
    payload = models.JSONField(default=dict, blank=True)
    status = models.CharField(
        max_length=20, choices=Status.choices, default=Status.PENDING)
    created_by = models.ForeignKey(
        settings.AUTH_USER_MODEL, on_delete=models.SET_NULL,
        null=True, blank=True, related_name='run_commands')
    created_at = models.DateTimeField(auto_now_add=True)
    resolved_at = models.DateTimeField(null=True, blank=True)

    class Meta:
        db_table = 'execution_run_commands'
        ordering = ['created_at']


class RunArtifact(models.Model):
    """A resource produced by a Run (file, image, doc...).

    Long-term reference is the provider artifact id (Aily: agent_artifact_id);
    download URLs are cached only and expire (Aily: 24h).
    """

    class StorageType(models.TextChoices):
        EXTERNAL = 'external', 'External (provider)'
        MIRRORED = 'mirrored', 'Mirrored object storage'

    class ResolutionStatus(models.TextChoices):
        PENDING = 'pending', 'Pending'
        RESOLVED = 'resolved', 'Resolved (URL cached)'
        EXPIRED = 'expired', 'Expired'
        FAILED = 'failed', 'Failed'
        MIRRORED = 'mirrored', 'Mirrored'

    id = models.UUIDField(primary_key=True, default=uuid.uuid4, editable=False)
    run = models.ForeignKey(Run, on_delete=models.CASCADE, related_name='artifacts')
    provider = models.CharField(max_length=64)
    # Aily: agent_artifact_id.
    external_artifact_id = models.CharField(max_length=255, blank=True, default='')
    provider_artifact_type = models.CharField(max_length=64, blank=True, default='')
    name = models.CharField(max_length=255, blank=True, default='')
    # Normalized cross-provider type: image, file, feishu_doc, bitable, ...
    normalized_type = models.CharField(max_length=32, blank=True, default='file')
    storage_type = models.CharField(
        max_length=20, choices=StorageType.choices, default=StorageType.EXTERNAL)
    cached_external_url = models.TextField(blank=True, default='')
    cached_url_fetched_at = models.DateTimeField(null=True, blank=True)
    cached_url_expires_at = models.DateTimeField(null=True, blank=True)
    storage_key = models.CharField(max_length=500, blank=True, default='')
    resolution_status = models.CharField(
        max_length=20, choices=ResolutionStatus.choices,
        default=ResolutionStatus.PENDING)
    metadata = models.JSONField(default=dict, blank=True)
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        db_table = 'execution_run_artifacts'
        ordering = ['created_at']
        indexes = [models.Index(fields=['run', 'created_at'])]
        constraints = [models.UniqueConstraint(
            fields=['run', 'external_artifact_id'],
            name='unique_run_artifact_external_id')]

    def __str__(self):
        return f'Artifact<{self.external_artifact_id or self.id}>'


class RuntimeAttachment(models.Model):
    """A user input file attached to a Run.

    Aily: agent_attachment_id. Attachments belong to the uploading user;
    upload and chat must share the same ProviderAuthContext.
    """

    class Type(models.TextChoices):
        IMAGE = 'image', 'Image'
        FILE = 'file', 'File'
        FEISHU_DOC = 'feishu_doc', 'Feishu doc'
        BITABLE = 'bitable', 'Bitable'

    class SourceType(models.TextChoices):
        UPLOAD = 'upload', 'Direct upload'
        DOC_URL = 'doc_url', 'Cloud doc reference'

    class Status(models.TextChoices):
        PENDING = 'pending', 'Pending'
        UPLOADED = 'uploaded', 'Uploaded to provider'
        FAILED = 'failed', 'Failed'

    id = models.UUIDField(primary_key=True, default=uuid.uuid4, editable=False)
    run = models.ForeignKey(
        Run, on_delete=models.CASCADE, null=True, blank=True,
        related_name='attachments')
    conversation = models.ForeignKey(
        'conversations.Conversation', on_delete=models.CASCADE,
        null=True, blank=True, related_name='attachments')
    provider = models.CharField(max_length=64)
    # Aily: agent_attachment_id.
    external_attachment_id = models.CharField(max_length=255, blank=True, default='')
    attachment_type = models.CharField(max_length=20, choices=Type.choices)
    name = models.CharField(max_length=255, blank=True, default='')
    source_type = models.CharField(
        max_length=20, choices=SourceType.choices, default=SourceType.UPLOAD)
    source_url = models.CharField(max_length=512, blank=True, default='')
    # Identity pinning so user A's attachment can never be replayed by user B.
    auth_mode = models.CharField(max_length=20, blank=True, default='')
    auth_subject_key = models.CharField(max_length=128, blank=True, default='')
    status = models.CharField(
        max_length=20, choices=Status.choices, default=Status.PENDING)
    metadata = models.JSONField(default=dict, blank=True)
    created_by = models.ForeignKey(
        settings.AUTH_USER_MODEL, on_delete=models.SET_NULL,
        null=True, blank=True, related_name='runtime_attachments')
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        db_table = 'execution_runtime_attachments'
        ordering = ['created_at']
        indexes = [models.Index(fields=['provider', 'external_attachment_id'])]


class RunLease(models.Model):
    """Worker claim over a Run using CAS + Lease (never SKIP LOCKED).

    A worker claims a queued run via a conditional UPDATE
    (status queued -> running); the lease row records ownership and
    heartbeats extend expires_at. Expired leases move the run to
    interrupted for retry or failure.
    """

    id = models.BigAutoField(primary_key=True)
    run = models.OneToOneField(Run, on_delete=models.CASCADE, related_name='lease')
    worker_id = models.CharField(max_length=128)
    lease_token = models.UUIDField(default=uuid.uuid4, editable=False)
    acquired_at = models.DateTimeField(auto_now_add=True)
    heartbeat_at = models.DateTimeField(null=True, blank=True)
    expires_at = models.DateTimeField(db_index=True)

    class Meta:
        db_table = 'execution_run_leases'
        ordering = ['acquired_at']

    def __str__(self):
        return f'Lease<{self.run_id}@{self.worker_id}>'
