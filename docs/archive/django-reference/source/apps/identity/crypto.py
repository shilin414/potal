"""Optional at-rest encryption for provider refresh tokens.

Enabled by setting IDENTITY_TOKEN_ENCRYPTION_KEY (Fernet key). Without a
key configured, values are stored as-is so development stays friction-free.
"""
from __future__ import annotations

from django.conf import settings


def _cipher():
    key = getattr(settings, 'IDENTITY_TOKEN_ENCRYPTION_KEY', '')
    if not key:
        return None
    from cryptography.fernet import Fernet
    return Fernet(key.encode() if isinstance(key, str) else key)


def encrypt_if_configured(value: str) -> str:
    cipher = _cipher()
    if cipher is None or not value:
        return value
    return 'enc:' + cipher.encrypt(value.encode()).decode()


def decrypt_if_configured(value: str) -> str:
    if not value or not value.startswith('enc:'):
        return value
    cipher = _cipher()
    if cipher is None:
        raise RuntimeError(
            'token is encrypted but IDENTITY_TOKEN_ENCRYPTION_KEY is unset')
    return cipher.decrypt(value[len('enc:'):].encode()).decode()
