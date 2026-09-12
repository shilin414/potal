"""
RuntimeAdapter protocol and RuntimeRegistry.

Every provider integration (Aily agent, Aily workflow, Codex, GraphFlow,
HTTP, ...) implements RuntimeAdapter and registers itself under a
`runtime_key` of the form `<provider_key>:<runtime_type>`.

Business modules resolve adapters through the registry — provider-specific
code must never leak into views, serializers, or models.
"""
from __future__ import annotations

import dataclasses
from typing import Any, AsyncIterator, Optional


@dataclasses.dataclass
class ProviderAuthContext:
    """Identity context for provider calls.

    `token` is an in-memory credential resolved per call from a secret
    backend / OAuth flow. It must never be persisted on Run, Conversation,
    Application, or RuntimeBinding rows.
    """

    provider: str
    identity_mode: str  # user | tenant
    subject_user_id: Optional[str] = None
    tenant_id: Optional[str] = None
    credential_ref: Optional[str] = None
    token: Optional[str] = dataclasses.field(default=None, repr=False)


@dataclasses.dataclass
class SubmitInput:
    """Normalized request to start a run on a provider."""

    run_id: str
    # Provider-resolved identity for the call.
    auth: ProviderAuthContext
    # Provider resource id (aily agent_id, workflow id, http endpoint key).
    external_resource_id: str
    # Opaque runtime payload; adapters translate it to provider requests.
    payload: dict
    # Provider session id for multi-turn conversations, if one exists.
    session_id: Optional[str] = None
    # Resolved provider attachment ids already uploaded under `auth`.
    external_attachment_ids: list[str] = dataclasses.field(default_factory=list)
    # Whether the caller wants streaming (interactive) execution.
    stream: bool = True
    timeout_seconds: int = 300


@dataclasses.dataclass
class SubmitResult:
    """What a provider returns when a run is started."""

    external_run_id: str
    # Session id returned/adopted by the provider (Aily lazy session).
    session_id: Optional[str] = None
    raw: dict = dataclasses.field(default_factory=dict)


@dataclasses.dataclass
class StatusResult:
    external_run_id: str
    provider_status: Optional[str] = None
    finish_reason: Optional[str] = None
    output: Optional[dict] = None
    raw: dict = dataclasses.field(default_factory=dict)


@dataclasses.dataclass
class StreamEvent:
    """A normalized provider event mapped to the unified event protocol."""

    event_type: str
    payload: dict = dataclasses.field(default_factory=dict)
    raw: dict = dataclasses.field(default_factory=dict)


@dataclasses.dataclass
class ArtifactRef:
    external_artifact_id: str
    provider_artifact_type: str = ''
    name: str = ''
    url: str = ''


class RuntimeAdapter:
    """Base class every provider integration adapter extends."""

    #: registry key of the form "<provider_key>:<runtime_type>"
    runtime_key: str = ''
    #: capability flags the provider declares (see architecture doc §15)
    capabilities: dict = {}

    # ------------------------------------------------------------------
    # Catalog descriptors (architecture doc §16)
    #
    # The 智能体市场 authors applications through the runtime registry, so the
    # *shape of a provider-side resource id* is declared here — by the adapter
    # that owns it — instead of being special-cased in views/serializers with
    # `if provider_key == "..."`.
    # ------------------------------------------------------------------
    #: Human label for the marketplace "新建智能体" picker.
    display_label: str = ''
    #: Label of the resource id field ("Agent ID", "Workflow ID", ...).
    resource_id_label: str = ''
    #: Regex the resource id must match; '' means "no local check".
    resource_id_pattern: str = ''
    #: One-line guidance shown next to the resource id field.
    resource_id_hint: str = ''

    # ------------------------------------------------------------------
    # Capabilities
    # ------------------------------------------------------------------
    def supports(self, capability: str) -> bool:
        return bool(self.capabilities.get(capability))

    # ------------------------------------------------------------------
    # Lifecycle
    # ------------------------------------------------------------------
    async def submit(self, submit: SubmitInput) -> SubmitResult:
        raise NotImplementedError

    async def get_status(self, auth: ProviderAuthContext,
                         external_resource_id: str,
                         external_run_id: str) -> StatusResult:
        raise NotImplementedError

    async def stream_events(self, submit: SubmitInput) -> AsyncIterator[StreamEvent]:
        raise NotImplementedError(
            f'{self.runtime_key} does not support streaming')
        yield  # pragma: no cover

    async def cancel(self, auth: ProviderAuthContext, external_run_id: str) -> bool:
        """Return True if the provider run was actually cancelled."""
        raise NotImplementedError(
            f'{self.runtime_key} does not support cancel')

    async def upload_attachment(self, auth: ProviderAuthContext,
                                external_resource_id: str,
                                *, file_bytes: Optional[bytes] = None,
                                filename: str = '',
                                attachment_type: str = 'file',
                                doc_url: str = '') -> str:
        """Upload an input attachment, return the provider attachment id."""
        raise NotImplementedError(
            f'{self.runtime_key} does not support attachments')

    async def resolve_artifact(self, auth: ProviderAuthContext,
                               external_resource_id: str,
                               external_artifact_id: str) -> ArtifactRef:
        """Resolve a provider artifact to a (temporary) download URL."""
        raise NotImplementedError(
            f'{self.runtime_key} does not support artifacts')

    async def build_auth(self, user, identity_mode: str = 'user') -> ProviderAuthContext:
        """Resolve the caller's provider credentials.

        Credential acquisition is provider-specific, so it belongs to the
        adapter; callers ask the registry instead of importing a provider
        module (architecture doc §49/§50). Implementations must never persist
        the token — it lives in memory for the duration of the call.

        Raise ``LookupError`` when the caller simply *has no* credential for
        this provider (not logged in via the provider, no refresh token yet):
        that is a "cannot check", not a "misconfigured", and callers degrade
        accordingly.
        """
        raise NotImplementedError(
            f'{self.runtime_key} does not implement credential resolution')

    async def check_visibility(self, auth: ProviderAuthContext,
                               external_resource_id: str) -> bool:
        raise NotImplementedError(
            f'{self.runtime_key} does not support visibility checks')


class RuntimeRegistry:
    """Maps runtime keys to adapter instances.

    Keys follow `provider_key:runtime_type`, e.g. `feishu_aily:agent`.
    The `provider_key` alone also resolves when a provider has a single
    canonical runtime (e.g. `codex`).
    """

    def __init__(self) -> None:
        self._adapters: dict[str, RuntimeAdapter] = {}

    def register(self, adapter: RuntimeAdapter, key: str | None = None) -> None:
        runtime_key = key or adapter.runtime_key
        if not runtime_key:
            raise ValueError('adapter must declare runtime_key')
        self._adapters[runtime_key] = adapter

    def resolve(self, provider_key: str, runtime_type: str) -> RuntimeAdapter:
        key = f'{provider_key}:{runtime_type}'
        adapter = self._adapters.get(key)
        if adapter is None:
            adapter = self._adapters.get(provider_key)
        if adapter is None:
            raise KeyError(
                f'no RuntimeAdapter registered for {key!r}')
        return adapter

    def has(self, provider_key: str, runtime_type: str) -> bool:
        try:
            self.resolve(provider_key, runtime_type)
            return True
        except KeyError:
            return False

    def all_keys(self) -> list[str]:
        return sorted(self._adapters)


registry = RuntimeRegistry()
