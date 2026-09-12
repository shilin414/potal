"""
Map Aily chat-result / SSE payloads to the unified event protocol.

Aily chat result shape (获取对话结果):
    {'content': [{'type': 'text', 'text': ..., 'agent_artifact_id': ...,
                  'artifact_type': ...}],
     'finish_reason': 'stop', 'status': 'Completed'}

Aily statuses observed in docs: Running / Completed / Failed / Cancelled.
"""
from __future__ import annotations

from typing import Optional

from apps.execution.services import (
    EVENT_ARTIFACT_DISCOVERED,
    EVENT_CONTENT_COMPLETED,
    EVENT_CONTENT_DELTA,
    EVENT_CONTENT_STARTED,
    EVENT_RUN_COMPLETED,
    EVENT_RUN_FAILED,
)

# Official status values seen across the local Aily docs.
_STATUS_RUNNING = ('running', 'in_progress', 'generating', 'opening')
_STATUS_SUCCEEDED = ('completed', 'succeeded', 'done', 'stop')
_STATUS_FAILED = ('failed', 'error')
_STATUS_CANCELLED = ('cancelled', 'canceled')


def map_provider_status(raw_status: str) -> Optional[str]:
    """Map an Aily status string to a unified Run status (None=keep)."""
    status = (raw_status or '').strip().lower()
    if status in _STATUS_RUNNING:
        return 'running'
    if status in _STATUS_SUCCEEDED:
        return 'succeeded'
    if status in _STATUS_FAILED:
        return 'failed'
    if status in _STATUS_CANCELLED:
        return 'cancelled'
    return None


def extract_text_items(chat_result: dict) -> list[dict]:
    return [c for c in chat_result.get('content') or []
            if (c.get('type') or '') == 'text']


def extract_final_text(chat_result: dict) -> str:
    return ''.join(c.get('text') or '' for c in extract_text_items(chat_result))


def extract_artifacts(chat_result: dict) -> list[dict]:
    """Content entries carrying agent_artifact_id (docs: 产物).

    Aily splits a generated file into two content entries: a text entry with
    the markdown `![alt](artifacts/<file>)` and a separate artifact entry
    (type image/file) whose own text is empty. So the filename is matched
    from the full message text, in content order.
    """
    import re

    def _filename_from_markdown(text: str) -> str:
        match = re.search(
            r'\((?:\./)?artifacts?/(?:[^)/]+/)*([^)/]+\.\w+)\)', text or '')
        return match.group(1) if match else ''

    artifacts = []
    pending_names = [
        _filename_from_markdown(item.get('text') or '')
        for item in chat_result.get('content') or []
        if not item.get('agent_artifact_id')
    ]
    for item in chat_result.get('content') or []:
        artifact_id = item.get('agent_artifact_id')
        if not artifact_id:
            continue
        name = item.get('name') or ''
        if not name:
            # Pair with the first unused markdown filename.
            while pending_names:
                candidate = pending_names.pop(0)
                if candidate:
                    name = candidate
                    break
        artifacts.append({
            'external_artifact_id': artifact_id,
            'provider_artifact_type': item.get('artifact_type') or '',
            'name': name,
            'url': item.get('url') or '',
        })
    return artifacts


def normalized_artifact_type(provider_artifact_type: str) -> str:
    """Map Aily artifact_type to the platform normalized_type."""
    t = (provider_artifact_type or '').lower()
    if not t:
        return 'file'
    if 'image' in t or 'picture' in t:
        return 'image'
    if 'sandbox_file' in t or 'file' in t:
        return 'file'
    if 'feishu' in t or 'doc' in t or 'wiki' in t:
        return 'feishu_doc'
    if 'bitable' in t or 'sheet' in t or 'base' in t:
        return 'bitable'
    if 'video' in t:
        return 'video'
    return 'file'


# ---------------------------------------------------------------------------
# SSE line parsing
# ---------------------------------------------------------------------------

def _extract_text(data: dict) -> str:
    """Pull the text out of an Aily SSE payload field.

    Observed in the wild: delta events carry text as a nested content object
    {'text': {'text': '...', 'type': 'content'}}; tolerate both that and a
    plain string so the frontend always receives a string delta.
    """
    value = data.get('text') or data.get('content') or data.get('delta')
    if isinstance(value, dict):
        inner = value.get('text') or value.get('content')
        return inner if isinstance(inner, str) else ''
    return value if isinstance(value, str) else ''


def parse_sse_line(line: str) -> Optional[dict]:
    """Parse one raw SSE line ('data: {...}' / 'event: x') into an event."""
    if line.startswith('data:'):
        payload = line[len('data:'):].strip()
        if not payload:
            return None
        import json
        try:
            return {'kind': 'data', 'data': json.loads(payload)}
        except (ValueError, json.JSONDecodeError):
            return {'kind': 'data', 'data': {'raw': payload}}
    if line.startswith('event:'):
        return {'kind': 'event', 'name': line[len('event:'):].strip()}
    return None


def sse_to_unified(event_name: str, data: dict) -> list[tuple[str, dict]]:
    """Translate a parsed Aily SSE event to unified (event_type, payload).

    Aily's stream contract is not fully documented; the mapper tolerates
    both explicit delta events and plain text chunks. Final authority is
    always get_chat_result reconciliation.
    """
    name = (event_name or '').lower()
    out: list[tuple[str, dict]] = []

    if 'delta' in name or 'message' in name or 'text' in name:
        text = _extract_text(data)
        if text:
            out.append((EVENT_CONTENT_DELTA, {'text': text}))
    elif 'start' in name or 'begin' in name:
        out.append((EVENT_CONTENT_STARTED, {}))
    elif 'done' in name or 'complete' in name or 'finish' in name or 'end' in name:
        text = _extract_text(data)
        if text:
            out.append((EVENT_CONTENT_DELTA, {'text': text}))
        out.append((EVENT_CONTENT_COMPLETED, {'finish_reason': data.get('finish_reason', '')}))
    elif 'error' in name or 'fail' in name:
        out.append((EVENT_RUN_FAILED, {
            'error_code': data.get('code') or 'aily_stream_error',
            'error_message': data.get('msg') or data.get('message') or '',
        }))
    else:
        # Unknown event: pass through as a delta if it carries text.
        text = _extract_text(data)
        if text:
            out.append((EVENT_CONTENT_DELTA, {'text': text}))

    artifact_id = data.get('agent_artifact_id')
    if artifact_id:
        out.append((EVENT_ARTIFACT_DISCOVERED, {
            'external_artifact_id': artifact_id,
            'provider_artifact_type': data.get('artifact_type') or '',
        }))
    return out


def completion_event(status: str) -> tuple[str, dict]:
    if status == 'failed':
        return EVENT_RUN_FAILED, {'status': status}
    return EVENT_RUN_COMPLETED, {'status': status}
