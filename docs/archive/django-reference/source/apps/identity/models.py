"""
Identity models: FeishuIdentity.

The unified User model stays in apps.users; this app adds the Feishu
identity link and the OAuth token store. Refresh tokens are stored only
to renew UATs for provider calls — access tokens live in Redis, never in
the database (and never in Run/Conversation/Application rows).
"""
import logging
import uuid

from django.conf import settings
from django.db import models
from django.utils import timezone

logger = logging.getLogger(__name__)


class FeishuIdentity(models.Model):
    """Links a local User to a Feishu account (auth_source=feishu).

    The refresh token is the only persisted provider credential; it is
    encrypted-at-rest in production deployments and rotated whenever the
    Feishu token endpoint returns a new one.
    """

    id = models.UUIDField(primary_key=True, default=uuid.uuid4, editable=False)
    user = models.OneToOneField(
        settings.AUTH_USER_MODEL, on_delete=models.CASCADE,
        related_name='feishu_identity')
    # Feishu identifiers (any subset may be present depending on scope).
    feishu_user_id = models.CharField(
        max_length=64, null=True, blank=True, unique=True)
    open_id = models.CharField(
        max_length=64, null=True, blank=True, unique=True, db_index=True)
    union_id = models.CharField(max_length=64, blank=True, default='')
    # Display snapshot updated at login.
    display_name = models.CharField(max_length=255, blank=True, default='')
    avatar_url = models.CharField(max_length=1000, blank=True, default='')
    # OAuth tokens (refresh only rotates rarely; access tokens are cached
    # in Redis by integrations.aily.auth).
    refresh_token = models.TextField(blank=True, default='')
    refresh_token_expires_at = models.DateTimeField(null=True, blank=True)
    last_login_at = models.DateTimeField(null=True, blank=True)
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        db_table = 'identity_feishu_identities'

    def __str__(self):
        return f'FeishuIdentity<{self.open_id or self.feishu_user_id}>'

    async def rotate_refresh_token(self, new_refresh: str) -> None:
        """Persist a rotated refresh token (fire-and-forget safe)."""
        from apps.identity.crypto import encrypt_if_configured
        await FeishuIdentity.objects.filter(pk=self.pk).aupdate(
            refresh_token=encrypt_if_configured(new_refresh),
            updated_at=timezone.now())

    def decrypted_refresh_token(self) -> str:
        if not self.refresh_token:
            return ''
        from apps.identity.crypto import decrypt_if_configured
        return decrypt_if_configured(self.refresh_token)
