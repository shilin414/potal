"""
Artifact resolver — external_refresh policy (architecture doc §74).

用户点击产物 -> 校验权限 -> 缓存 URL 仍有效 -> 302
                                    -> 过期 -> 调 Aily 产物接口取新 24h URL -> 302

The provider artifact id is the long-term reference; the signed URL is a
cache with an expiry and must never be treated as a permanent address.
"""
from __future__ import annotations

import logging
from datetime import timedelta

from django.utils import timezone

from apps.catalog.runtime import registry
from apps.execution.models import RunArtifact
from integrations.aily import auth as aily_auth

logger = logging.getLogger(__name__)

# Aily artifact URLs are valid for 24 hours per official docs.
URL_TTL = timedelta(hours=23)  # refresh slightly before the 24h boundary


class ArtifactResolutionError(Exception):
    pass


async def resolve_artifact_url(artifact: RunArtifact, user) -> str:
    """Return a currently-valid download URL for the artifact.

    Uses the cached URL when it is still fresh; otherwise re-resolves via
    the provider adapter with the run owner's auth context.
    """
    if artifact.cached_external_url and artifact.cached_url_expires_at \
            and artifact.cached_url_expires_at > timezone.now() + timedelta(minutes=5):
        return artifact.cached_external_url

    if artifact.provider != 'feishu_aily':
        raise ArtifactResolutionError(
            f'no resolver for provider {artifact.provider}')

    # Lazy FK access (artifact.run / run.runtime_binding / run.user) is
    # synchronous ORM and raises SynchronousOnlyOperation inside this
    # coroutine — load the whole run through sync_to_async once.
    from asgiref.sync import sync_to_async

    @sync_to_async
    def _load_run_context():
        run = artifact.run
        binding_id = (run.runtime_binding.external_resource_id
                      if run.runtime_binding else '')
        return run, (run.runtime_snapshot or {}), binding_id, run.user

    run, snapshot, binding_agent_id, run_user = await _load_run_context()
    agent_id = snapshot.get('external_resource_id') or binding_agent_id
    if not agent_id:
        raise ArtifactResolutionError('missing agent_id for artifact owner run')

    identity_mode = snapshot.get('identity_mode') or 'user'
    auth = await aily_auth.build_auth_context(
        run_user, identity_mode=identity_mode)
    from django_redis import get_redis_connection
    from integrations.aily.rate_limit import artifacts_limiter
    redis = await sync_to_async(get_redis_connection)('default')
    await artifacts_limiter(redis).acquire_async()
    adapter = registry.resolve(artifact.provider, 'agent')
    ref = await adapter.resolve_artifact(
        auth, agent_id, artifact.external_artifact_id)

    if not ref.url:
        raise ArtifactResolutionError(
            'provider returned no artifact url')

    await RunArtifact.objects.filter(pk=artifact.pk).aupdate(
        cached_external_url=ref.url,
        cached_url_fetched_at=timezone.now(),
        cached_url_expires_at=timezone.now() + URL_TTL,
        name=ref.name or artifact.name,
        resolution_status=RunArtifact.ResolutionStatus.RESOLVED,
    )
    return ref.url
