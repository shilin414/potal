"""Display identity of a user (name + user_id) for UI surfaces.

The chat UI shows a message sender as ``姓名（user_id）``. Both halves come
from different places depending on how the user signed in:

* ``display_name`` — the Feishu profile name, else ``first_name``, else the
  local ``username``.
* ``display_id``   — ``User.display_id`` when an operator set it, else the
  Feishu ``user_id`` learned at login, else the local primary key. It is a
  *display* identifier: never use it for authorization or identity matching.

Kept in one place so the login response, the OAuth exchange, the session
endpoint and the DRF user serializers can never disagree (they used to).
"""
from __future__ import annotations

from typing import Any, Optional


def feishu_identity_of(user) -> Optional[Any]:
    """Return the FeishuIdentity link of ``user``, or None.

    ``getattr`` is safe here: Django's reverse one-to-one raises
    ``RelatedObjectDoesNotExist``, which subclasses ``AttributeError``.
    No import from apps.identity, so the users app stays import-free of it.
    """
    return getattr(user, 'feishu_identity', None)


def display_identity(user) -> dict:
    """Return ``{'display_name': str, 'display_id': str}`` for display."""
    if user is None:
        return {'display_name': '', 'display_id': ''}
    identity = feishu_identity_of(user)
    name = (
        (getattr(identity, 'display_name', '') or '')
        or (getattr(user, 'first_name', '') or '')
        or (getattr(user, 'username', '') or '')
    )
    external_id = (
        (getattr(user, 'display_id', '') or '')
        or (getattr(identity, 'feishu_user_id', '') or '')
    )
    return {
        'display_name': name,
        'display_id': str(external_id or user.pk),
    }


def session_user_payload(user) -> dict:
    """The user object every login / exchange / session endpoint returns.

    One shared shape on purpose: the frontend persists this object and renders
    the chat sender from it, so a field present in only one endpoint would show
    up as ``undefined`` in the UI depending on how the user signed in.
    """
    identity = feishu_identity_of(user)
    return {
        'id': user.pk,
        'username': user.username,
        'email': getattr(user, 'email', '') or '',
        'role': getattr(user, 'role', '') or '',
        'avatar': getattr(user, 'avatar', '') or '',
        'avatar_url': (
            (getattr(identity, 'avatar_url', '') or '')
            or (getattr(user, 'avatar', '') or '')),
        'auth_source': getattr(user, 'auth_source', '') or '',
        'is_staff': bool(getattr(user, 'is_staff', False)),
        **display_identity(user),
    }
