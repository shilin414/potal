"""Persistence helpers for Codex-shaped thread, turn, item, and request state."""
from __future__ import annotations

import json
from typing import Iterable

from django.db import transaction
from django.utils import timezone

from core.agent_engine.protocol import ProtocolEvent, ProtocolMethod

from .models import (
    AgentItem,
    AgentServerRequest,
    AgentThread,
    AgentTurn,
    Conversation,
)


def ensure_agent_thread(
    conversation: Conversation,
    *,
    provider: str,
    remote_id: str = '',
    config: dict | None = None,
) -> AgentThread:
    """Return the product thread associated with a conversation."""
    thread, _created = AgentThread.objects.get_or_create(
        conversation=conversation,
        defaults={
            'provider': provider,
            'remote_id': remote_id,
            'status': AgentThread.Status.ACTIVE,
            'config': config or {},
        },
    )
    changed = []
    if thread.provider != provider:
        thread.provider = provider
        thread.remote_id = remote_id
        changed.extend(['provider', 'remote_id'])
    elif remote_id and thread.remote_id != remote_id:
        thread.remote_id = remote_id
        changed.append('remote_id')
    if thread.status != AgentThread.Status.ACTIVE:
        thread.status = AgentThread.Status.ACTIVE
        changed.append('status')
    if config is not None and thread.config != config:
        thread.config = config
        changed.append('config')
    if changed:
        thread.save(update_fields=[*changed, 'updated_at'])
    return thread


def start_agent_turn(
    thread: AgentThread,
    *,
    text: str,
    remote_id: str = '',
) -> AgentTurn:
    return AgentTurn.objects.create(
        thread=thread,
        remote_id=remote_id,
        status=AgentTurn.Status.IN_PROGRESS,
        input=[{'type': 'text', 'text': text}],
    )


def update_turn_remote_id(turn: AgentTurn, remote_id: str) -> None:
    if remote_id and not turn.remote_id:
        AgentTurn.objects.filter(pk=turn.pk, remote_id='').update(remote_id=remote_id)
        turn.remote_id = remote_id


def record_server_requests(
    thread: AgentThread,
    turn: AgentTurn,
    events: Iterable[ProtocolEvent],
) -> None:
    request_methods = {
        ProtocolMethod.REQUEST_USER_INPUT,
        ProtocolMethod.COMMAND_APPROVAL,
    }
    for event in events:
        if event.method not in request_methods:
            continue
        remote_id = str(event.params.get('requestId') or '')
        AgentServerRequest.objects.get_or_create(
            thread=thread,
            turn=turn,
            remote_id=remote_id,
            method=event.method,
            defaults={'params': event.params},
        )


@transaction.atomic
def finish_agent_turn(
    turn: AgentTurn,
    *,
    status: str,
    items: Iterable[dict],
    usage: dict | None = None,
    error: dict | None = None,
) -> None:
    """Persist the final item projection and terminal turn state atomically."""
    for ordinal, item in enumerate(items):
        remote_id = str(item.get('id') or f'item-{ordinal}')
        AgentItem.objects.update_or_create(
            turn=turn,
            remote_id=remote_id,
            defaults={
                'item_type': str(item.get('type') or 'unknown'),
                'status': str(item.get('status') or ''),
                'ordinal': ordinal,
                'content': _item_content(item),
                'payload': item,
            },
        )
    turn.status = status
    turn.usage = usage or {}
    turn.error = error or {}
    turn.completed_at = timezone.now()
    turn.save(update_fields=['status', 'usage', 'error', 'completed_at'])
    thread_status = (
        AgentThread.Status.ERROR
        if status == AgentTurn.Status.FAILED
        else AgentThread.Status.IDLE
    )
    AgentThread.objects.filter(pk=turn.thread_id).update(status=thread_status)


def _item_content(item: dict) -> str:
    item_type = item.get('type')
    if item_type == 'agentMessage':
        return str(item.get('text') or '')
    if item_type == 'reasoning':
        return '\n'.join(str(part) for part in item.get('content') or [])
    if item_type == 'commandExecution':
        return str(item.get('aggregatedOutput') or '')
    if item_type == 'webSearch':
        return str(item.get('query') or '')
    result = item.get('result')
    if isinstance(result, str):
        return result
    return json.dumps(result, ensure_ascii=False, default=str) if result is not None else ''
