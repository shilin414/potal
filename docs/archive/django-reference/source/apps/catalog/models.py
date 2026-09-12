"""
Catalog models: Provider and ApplicationRuntimeBinding.

The catalog implements the four-layer target architecture:

    Application  — what the user sees (kept in apps.applications)
    Runtime      — how it executes (runtime_type on RuntimeBinding)
    Provider     — who actually executes (Provider + provider_key)
    Renderer     — how it is displayed (renderer_key on Application)

ApplicationRuntimeBinding is the single place where runtime behavior is
described; business code must resolve execution through RuntimeRegistry,
never with `if provider == "aily"` scattered around.
"""
import uuid

from django.db import models

# ---------------------------------------------------------------------------
# Shared choice enums
# ---------------------------------------------------------------------------


class RuntimeType(models.TextChoices):
    AGENT = 'agent', 'Agent'
    WORKFLOW = 'workflow', 'Workflow'
    HTTP = 'http', 'HTTP'
    LOCAL_TASK = 'local_task', 'Local Task'
    MEDIA = 'media', 'Media'
    NONE = 'none', 'None (fixed page)'


class IdentityMode(models.TextChoices):
    USER = 'user', 'User identity (UAT)'
    TENANT = 'tenant', 'App identity (TAT)'


class ExecutionMode(models.TextChoices):
    INTERACTIVE = 'interactive', 'Interactive (stream)'
    BACKGROUND = 'background', 'Background (poll)'


class SessionPolicy(models.TextChoices):
    LAZY = 'lazy', 'Lazy (create on first message)'
    EAGER = 'eager', 'Eager (create on open)'


class ArtifactPolicy(models.TextChoices):
    EXTERNAL_REFRESH = 'external_refresh', 'Refresh URL on access'
    MIRROR_ON_ACCESS = 'mirror_on_access', 'Mirror on first access'
    MIRROR_ON_COMPLETE = 'mirror_on_complete', 'Mirror on run completion'


# ---------------------------------------------------------------------------
# Provider
# ---------------------------------------------------------------------------


class Provider(models.Model):
    """An external runtime provider (feishu_aily, codex, graphflow, ...).

    Provider rows are configuration/catalog data: which providers exist,
    their capabilities and rate limits. Credentials are NEVER stored here —
    `secret_ref` names a SecretReference (or environment secret).
    """

    class Status(models.TextChoices):
        ACTIVE = 'active', 'Active'
        DISABLED = 'disabled', 'Disabled'

    id = models.UUIDField(primary_key=True, default=uuid.uuid4, editable=False)
    key = models.CharField(max_length=64, unique=True, db_index=True)
    name = models.CharField(max_length=120)
    description = models.TextField(blank=True, default='')
    # One provider may serve several runtime types (aily: agent + workflow).
    supported_runtime_types = models.JSONField(default=list, blank=True)
    # Declared capabilities; drives frontend feature visibility.
    capabilities = models.JSONField(default=dict, blank=True)
    # Rate limit / concurrency policy for the dispatcher.
    start_rate_limit = models.CharField(max_length=32, blank=True, default='')
    max_inflight = models.PositiveIntegerField(default=100)
    poll_rate_limit = models.CharField(max_length=32, blank=True, default='')
    artifact_rate_limit = models.CharField(max_length=32, blank=True, default='')
    timeout_seconds = models.PositiveIntegerField(default=300)
    retry_policy = models.JSONField(default=dict, blank=True)
    circuit_breaker = models.JSONField(default=dict, blank=True)
    # Points to a SecretReference name or environment variable name.
    secret_ref = models.CharField(max_length=200, blank=True, default='')
    base_url = models.CharField(max_length=500, blank=True, default='')
    status = models.CharField(
        max_length=20, choices=Status.choices, default=Status.ACTIVE)
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        db_table = 'catalog_providers'
        ordering = ['key']

    def __str__(self):
        return f'{self.key}'


# ---------------------------------------------------------------------------
# ApplicationRuntimeBinding
# ---------------------------------------------------------------------------


class ApplicationRuntimeBinding(models.Model):
    """How a specific Application executes on a specific Provider.

    Aily resource mapping (per official docs):
        external_resource_id  <-> aily agent_id
        AgentThread.remote_id <-> aily session_id
        Run.external_run_id    <-> aily agent_chat_id
    """

    id = models.UUIDField(primary_key=True, default=uuid.uuid4, editable=False)
    application = models.ForeignKey(
        'applications.Application', on_delete=models.CASCADE,
        related_name='runtime_bindings')
    runtime_type = models.CharField(
        max_length=20, choices=RuntimeType.choices, default=RuntimeType.AGENT)
    provider = models.ForeignKey(
        Provider, on_delete=models.PROTECT, related_name='runtime_bindings',
        null=True, blank=True)
    # Denormalized for query/index stability when the Provider row is absent.
    provider_key = models.CharField(max_length=64, db_index=True)
    # e.g. aily agent_id, aily workflow id, http endpoint key.
    external_resource_id = models.CharField(max_length=255, blank=True, default='')
    endpoint_key = models.CharField(max_length=120, blank=True, default='')

    identity_mode = models.CharField(
        max_length=20, choices=IdentityMode.choices, default=IdentityMode.USER)
    execution_mode = models.CharField(
        max_length=20, choices=ExecutionMode.choices,
        default=ExecutionMode.INTERACTIVE)
    session_policy = models.CharField(
        max_length=20, choices=SessionPolicy.choices, default=SessionPolicy.LAZY)
    artifact_policy = models.CharField(
        max_length=30, choices=ArtifactPolicy.choices,
        default=ArtifactPolicy.EXTERNAL_REFRESH)

    capabilities = models.JSONField(default=dict, blank=True)
    input_schema = models.JSONField(default=dict, blank=True)
    output_schema = models.JSONField(default=dict, blank=True)
    config = models.JSONField(default=dict, blank=True)
    secret_ref = models.CharField(max_length=200, blank=True, default='')
    timeout_seconds = models.PositiveIntegerField(default=300)

    enabled = models.BooleanField(default=True)
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        db_table = 'catalog_runtime_bindings'
        ordering = ['application_id', 'id']
        indexes = [
            models.Index(fields=['provider_key', 'runtime_type']),
            models.Index(fields=['application', 'enabled']),
        ]
        constraints = [
            models.UniqueConstraint(
                fields=['application', 'runtime_type', 'provider_key'],
                name='unique_binding_per_runtime'),
        ]

    def __str__(self):
        return f'{self.application_id}:{self.runtime_type}@{self.provider_key}'

    def snapshot(self) -> dict:
        """Runtime snapshot stored on each Run (never contains secrets)."""
        return {
            'runtime_type': self.runtime_type,
            'provider_key': self.provider_key,
            'external_resource_id': self.external_resource_id,
            'identity_mode': self.identity_mode,
            'execution_mode': self.execution_mode,
            'session_policy': self.session_policy,
            'artifact_policy': self.artifact_policy,
            'timeout_seconds': self.timeout_seconds,
            'config': self.config,
        }
