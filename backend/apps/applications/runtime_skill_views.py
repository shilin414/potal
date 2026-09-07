"""Manage filesystem-backed skills for the supported agent runtimes."""
from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
import uuid
from datetime import datetime, timezone
from pathlib import Path

from django.conf import settings
from rest_framework import serializers, status
from rest_framework.exceptions import NotFound, PermissionDenied, ValidationError
from rest_framework.permissions import IsAuthenticated
from rest_framework.response import Response
from rest_framework.views import APIView


PROVIDER_LABELS = {
    'codex': 'Codex',
    'graphflow': 'GraphFlow',
}
MAX_SKILL_BYTES = 1024 * 1024
SKILL_SLUG = re.compile(r'^[A-Za-z0-9][A-Za-z0-9._-]{0,119}$')


def _configured_roots() -> dict[str, Path]:
    return {
        'codex': Path(settings.CODEX_SKILLS_DIRECTORY).expanduser(),
        'graphflow': Path(settings.GRAPHFLOW_SKILLS_DIRECTORY).expanduser(),
    }


def _root(provider: str) -> Path:
    try:
        return _configured_roots()[provider].resolve()
    except KeyError as exc:
        raise ValidationError({'provider': '不支持的技能运行时。'}) from exc


def _validate_slug(slug: str) -> str:
    value = str(slug or '').strip()
    if (
        not SKILL_SLUG.fullmatch(value)
        or value.startswith('.')
        or '..' in value
    ):
        raise ValidationError({
            'slug': '只能使用字母、数字、点、下划线和短横线，且不能以点开头。'
        })
    return value


def _skill_dir(provider: str, slug: str, *, must_exist: bool = True) -> Path:
    root = _root(provider)
    value = _validate_slug(slug)
    unresolved = root / value
    if unresolved.is_symlink():
        raise ValidationError({'slug': '不允许管理符号链接技能目录。'})
    resolved = unresolved.resolve()
    if resolved.parent != root:
        raise ValidationError({'slug': '技能目录必须是配置根目录的直接子目录。'})
    if must_exist and (not resolved.is_dir() or not (resolved / 'SKILL.md').is_file()):
        raise NotFound('技能不存在。')
    return resolved


def _read_skill_file(path: Path) -> str:
    if path.stat().st_size > MAX_SKILL_BYTES:
        raise ValidationError({'content': 'SKILL.md 不能超过 1 MB。'})
    try:
        return path.read_text(encoding='utf-8')
    except UnicodeDecodeError as exc:
        raise ValidationError({'content': 'SKILL.md 必须使用 UTF-8 编码。'}) from exc


def _frontmatter(content: str, key: str) -> str:
    block = re.match(r'^\s*---\s*\r?\n(.*?)\r?\n---(?:\r?\n|$)', content, re.DOTALL)
    source = block.group(1) if block else ''
    match = re.search(
        rf'^\s*{re.escape(key)}\s*:\s*(.*?)\s*$', source, re.MULTILINE)
    if not match:
        return ''
    return match.group(1).strip().strip('"\'')


def _skill_files(directory: Path) -> list[dict]:
    files = []
    for path in sorted(directory.rglob('*')):
        if not path.is_file() or path.is_symlink():
            continue
        try:
            resolved = path.resolve()
            resolved.relative_to(directory)
        except (OSError, ValueError):
            continue
        relative = resolved.relative_to(directory).as_posix()
        files.append({'path': relative, 'size': resolved.stat().st_size})
        if len(files) >= 200:
            break
    return files


def _serialize_skill(provider: str, directory: Path, *, detail: bool = False) -> dict:
    entrypoint = directory / 'SKILL.md'
    content = _read_skill_file(entrypoint)
    stat = entrypoint.stat()
    files = _skill_files(directory)
    payload = {
        'provider': provider,
        'providerLabel': PROVIDER_LABELS[provider],
        'slug': directory.name,
        'name': _frontmatter(content, 'name') or directory.name,
        'description': _frontmatter(content, 'description'),
        'path': str(directory),
        'entrypoint': str(entrypoint),
        'contentHash': hashlib.sha256(content.encode('utf-8')).hexdigest(),
        'updatedAt': datetime.fromtimestamp(
            stat.st_mtime, tz=timezone.utc).isoformat(),
        'fileCount': len(files),
    }
    if detail:
        payload['content'] = content
        payload['files'] = files
    return payload


def _list_skills(provider: str) -> list[dict]:
    root = _root(provider)
    if not root.is_dir():
        return []
    skills = []
    for directory in sorted(root.iterdir(), key=lambda item: item.name.lower()):
        if directory.name.startswith('.') or directory.is_symlink():
            continue
        try:
            resolved = directory.resolve()
        except OSError:
            continue
        if (
            resolved.parent == root
            and resolved.is_dir()
            and (resolved / 'SKILL.md').is_file()
        ):
            try:
                skills.append(_serialize_skill(provider, resolved))
            except (OSError, ValidationError):
                # One unreadable or malformed package must not hide every
                # healthy skill in the configured runtime root.
                continue
    return skills


def _can_manage(user) -> bool:
    return bool(
        user.is_superuser
        or getattr(user, 'role', '') == getattr(user.Role, 'ADMIN', 'admin')
    )


def _require_manager(request) -> None:
    if not _can_manage(request.user):
        raise PermissionDenied('只有管理员可以修改运行时技能。')


def _write_atomic(path: Path, content: str) -> None:
    encoded = content.encode('utf-8')
    if len(encoded) > MAX_SKILL_BYTES:
        raise ValidationError({'content': 'SKILL.md 不能超过 1 MB。'})
    temporary = path.with_name(f'.SKILL.{uuid.uuid4().hex}.tmp')
    try:
        temporary.write_bytes(encoded)
        os.replace(temporary, path)
    finally:
        if temporary.exists():
            temporary.unlink()


def _default_content(slug: str, name: str, description: str) -> str:
    return (
        '---\n'
        f'name: {json.dumps(name or slug, ensure_ascii=False)}\n'
        f'description: {json.dumps(description or "", ensure_ascii=False)}\n'
        '---\n\n'
        f'# {name or slug}\n\n'
        '在这里编写技能说明和执行规则。\n'
    )


class RuntimeSkillCreateSerializer(serializers.Serializer):
    provider = serializers.ChoiceField(choices=tuple(PROVIDER_LABELS))
    slug = serializers.CharField(max_length=120)
    name = serializers.CharField(max_length=120, required=False, allow_blank=True)
    description = serializers.CharField(required=False, allow_blank=True)
    content = serializers.CharField(
        required=False, allow_blank=False, max_length=MAX_SKILL_BYTES)

    def validate_slug(self, value):
        return _validate_slug(value)


class RuntimeSkillUpdateSerializer(serializers.Serializer):
    content = serializers.CharField(allow_blank=False, max_length=MAX_SKILL_BYTES)


class RuntimeSkillCollectionView(APIView):
    permission_classes = [IsAuthenticated]

    def get(self, request):
        roots = []
        skills = []
        for provider, label in PROVIDER_LABELS.items():
            root = _root(provider)
            provider_skills = _list_skills(provider)
            roots.append({
                'provider': provider,
                'label': label,
                'path': str(root),
                'exists': root.is_dir(),
                'skillCount': len(provider_skills),
            })
            skills.extend(provider_skills)
        return Response({
            'roots': roots,
            'skills': skills,
            'canManage': _can_manage(request.user),
        })

    def post(self, request):
        _require_manager(request)
        serializer = RuntimeSkillCreateSerializer(data=request.data)
        serializer.is_valid(raise_exception=True)
        values = serializer.validated_data
        directory = _skill_dir(
            values['provider'], values['slug'], must_exist=False)
        if directory.exists():
            return Response(
                {'detail': '同名技能目录已经存在。'},
                status=status.HTTP_409_CONFLICT,
            )
        root = _root(values['provider'])
        root.mkdir(parents=True, exist_ok=True)
        directory.mkdir()
        try:
            content = values.get('content') or _default_content(
                values['slug'], values.get('name', ''),
                values.get('description', ''))
            _write_atomic(directory / 'SKILL.md', content)
        except Exception:
            if directory.exists():
                shutil.rmtree(directory)
            raise
        return Response(
            _serialize_skill(values['provider'], directory, detail=True),
            status=status.HTTP_201_CREATED,
        )


class RuntimeSkillDetailView(APIView):
    permission_classes = [IsAuthenticated]

    def get(self, request, provider: str, slug: str):
        directory = _skill_dir(provider, slug)
        payload = _serialize_skill(provider, directory, detail=True)
        payload['canManage'] = _can_manage(request.user)
        return Response(payload)

    def patch(self, request, provider: str, slug: str):
        _require_manager(request)
        serializer = RuntimeSkillUpdateSerializer(data=request.data)
        serializer.is_valid(raise_exception=True)
        directory = _skill_dir(provider, slug)
        _write_atomic(directory / 'SKILL.md', serializer.validated_data['content'])
        return Response(_serialize_skill(provider, directory, detail=True))

    def put(self, request, provider: str, slug: str):
        return self.patch(request, provider, slug)

    def delete(self, request, provider: str, slug: str):
        _require_manager(request)
        directory = _skill_dir(provider, slug)
        shutil.rmtree(directory)
        return Response(status=status.HTTP_204_NO_CONTENT)
