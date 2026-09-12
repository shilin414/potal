"""v2 authoring API for 智能体市场 (the agent marketplace).

The marketplace authors **Applications** — the product entity the workspace
actually opens (architecture doc §6/§8) — and attaches the runtime binding in
the same transaction (§12). That single write is what turns "an Aily custom
agent" into something the workspace can run; the agent itself keeps living in
the provider console, Studio only references it by ``external_resource_id``.

Endpoints (mounted under ``/api/v2/`` by apps/execution/urls.py):

    POST   /api/v2/applications                       新建智能体
    PATCH  /api/v2/applications/{id}                  编辑智能体
    DELETE /api/v2/applications/{id}                  删除智能体
    GET    /api/v2/applications/{id}/avatar           读取头像
    POST   /api/v2/applications/{id}/avatar           修改头像 (multipart)
    DELETE /api/v2/applications/{id}/avatar           清除头像，回退 emoji 图标
    POST   /api/v2/applications/{id}/default-agent    设为主智能体 (§38)
    DELETE /api/v2/applications/{id}/default-agent    取消主智能体
    GET    /api/v2/runtimes                           可用的运行时（表单用它渲染）
    POST   /api/v2/runtimes/validate                  校验运行时资源可见性

Provider differences never leak into this module: label, resource-id shape,
hint and capabilities are read from the RuntimeAdapter that owns them through
RuntimeRegistry (§16), never from an ``if provider_key == ...`` branch.

``GET /api/v2/applications`` (the listing half of the collection) lives in
apps/execution/views.py next to the other read endpoints; its ``post`` method
delegates here.
"""
from __future__ import annotations

import asyncio
import logging
import mimetypes
import re
import uuid

from django.core.exceptions import ValidationError as DjangoValidationError
from django.db import transaction
from django.db.models import Max
from django.db.models.deletion import ProtectedError
from django.http import FileResponse
from django.urls import path, reverse
from django.utils.text import slugify
from rest_framework import serializers, status
from rest_framework.exceptions import NotFound, PermissionDenied, ValidationError
from rest_framework.permissions import IsAuthenticated
from rest_framework.response import Response
from rest_framework.views import APIView

from apps.applications.models import (
    Application,
    ApplicationCategory,
    AVATAR_ALLOWED_EXTENSIONS,
    AVATAR_MAX_BYTES,
)
from apps.catalog.models import (
    ApplicationRuntimeBinding,
    ArtifactPolicy,
    ExecutionMode,
    IdentityMode,
    Provider,
    RuntimeType,
    SessionPolicy,
)
from apps.catalog.runtime import registry

logger = logging.getLogger(__name__)

SLUG_PATTERN = re.compile(r'^[a-z0-9]+(?:-[a-z0-9]+)*$')
DEFAULT_CATEGORY_SLUG = 'agents'
DEFAULT_CATEGORY_NAME = '智能体'


# ---------------------------------------------------------------------------
# Shared helpers
# ---------------------------------------------------------------------------

def get_application(application_id: int) -> Application:
    application = Application.objects.filter(pk=application_id).first()
    if application is None:
        raise NotFound('application not found')
    return application


def can_manage(request, application: Application) -> bool:
    """Only the creator (or staff) may edit/delete/promote an application."""
    user = request.user
    return bool(user.is_staff or application.created_by_id == user.pk)


def require_manage(request, application: Application) -> None:
    if not can_manage(request, application):
        raise PermissionDenied('只有应用创建者或管理员可以修改该智能体。')


def avatar_url(application: Application) -> str:
    """Relative, auth-checked URL of the avatar ('' when unset).

    Avatars are served by ``ApplicationAvatarView`` rather than ``MEDIA_URL``
    so they ride the same ``/api`` proxy + session cookie as every other call
    (and are not world-readable). ``v`` busts the browser cache on replace.
    """
    if not application.avatar:
        return ''
    path = reverse('v2-application-avatar',
                   kwargs={'application_id': application.pk})
    return f'{path}?v={int(application.updated_at.timestamp())}'


def serialize_application(request, application: Application) -> dict:
    """Compact application payload shared by the authoring endpoints."""
    bindings = list(application.runtime_bindings.all())
    binding = bindings[0] if bindings else None
    return {
        'id': application.pk,
        'slug': application.slug,
        'name': application.name,
        'description': application.description,
        'icon': application.icon,
        'avatar_url': avatar_url(application),
        'color': application.color,
        'kind': application.kind,
        'is_public': application.is_public,
        'category_slug': application.category.slug,
        'category_name': application.category.name,
        'is_default_agent': application.is_default_agent,
        'is_bound': binding is not None and binding.enabled,
        'runtime_type': binding.runtime_type if binding else '',
        'provider_key': binding.provider_key if binding else '',
        'external_resource_id': binding.external_resource_id if binding else '',
        'identity_mode': binding.identity_mode if binding else '',
        'execution_mode': binding.execution_mode if binding else '',
        'can_manage': can_manage(request, application),
        'updated_at': application.updated_at,
    }


def resolve_category(category_slug: str = '', category_name: str = '') -> ApplicationCategory:
    """Map the marketplace category onto an ApplicationCategory.

    Application categories and legacy agent categories are different tables;
    the marketplace passes the *agent* category slug so the same rail filters
    both lists. Categories are created on demand (idempotent by slug).
    """
    slug = slugify((category_slug or '').strip()) or DEFAULT_CATEGORY_SLUG
    name = (category_name or '').strip() or DEFAULT_CATEGORY_NAME
    category = ApplicationCategory.objects.filter(slug=slug).first()
    if category is not None:
        return category
    if ApplicationCategory.objects.filter(name=name).exists():
        # name is unique; fall back to the slug so creation cannot 500.
        name = slug
    next_order = (ApplicationCategory.objects.aggregate(
        top=Max('order'))['top'] or 0) + 1
    return ApplicationCategory.objects.create(
        name=name, slug=slug, order=next_order)


def resolve_runtime(provider_key: str, runtime_type: str):
    """Validate a (provider, runtime) pair and return (Provider, adapter)."""
    provider = Provider.objects.filter(key=provider_key).first()
    if provider is None:
        raise ValidationError(
            {'provider_key': f'未知的运行时提供方：{provider_key}'})
    if provider.status != Provider.Status.ACTIVE:
        raise ValidationError(
            {'provider_key': f'运行时提供方已停用：{provider_key}'})
    supported = list(provider.supported_runtime_types or [])
    if supported and runtime_type not in supported:
        raise ValidationError(
            {'runtime_type': f'{provider_key} 不支持 {runtime_type} 运行时'})
    if not registry.has(provider_key, runtime_type):
        raise ValidationError(
            {'runtime_type': f'运行时适配器未注册：{provider_key}:{runtime_type}'})
    return provider, registry.resolve(provider_key, runtime_type)


def validate_resource_id(adapter, external_resource_id: str) -> str:
    """Check the provider-side resource id against the adapter's declaration."""
    value = (external_resource_id or '').strip()
    if adapter.resource_id_label and not value:
        raise ValidationError(
            {'external_resource_id': f'{adapter.resource_id_label} 不能为空'})
    pattern = getattr(adapter, 'resource_id_pattern', '') or ''
    if value and pattern and not re.match(pattern, value):
        raise ValidationError({'external_resource_id': (
            adapter.resource_id_hint
            or f'{adapter.resource_id_label or "资源 ID"} 格式不正确')})
    return value


# ---------------------------------------------------------------------------
# Request serializers
# ---------------------------------------------------------------------------

class RuntimeBindingInputSerializer(serializers.Serializer):
    """The `runtime` block of a create/update request."""

    provider_key = serializers.CharField(max_length=64)
    runtime_type = serializers.ChoiceField(
        choices=RuntimeType.choices, default=RuntimeType.AGENT)
    external_resource_id = serializers.CharField(
        max_length=255, required=False, allow_blank=True, default='')
    identity_mode = serializers.ChoiceField(
        choices=IdentityMode.choices, default=IdentityMode.USER)
    execution_mode = serializers.ChoiceField(
        choices=ExecutionMode.choices, default=ExecutionMode.INTERACTIVE)
    session_policy = serializers.ChoiceField(
        choices=SessionPolicy.choices, default=SessionPolicy.LAZY)
    artifact_policy = serializers.ChoiceField(
        choices=ArtifactPolicy.choices, default=ArtifactPolicy.EXTERNAL_REFRESH)
    timeout_seconds = serializers.IntegerField(
        required=False, min_value=10, max_value=3600, default=300)
    config = serializers.DictField(required=False, default=dict)

    def validate(self, attrs):
        provider, adapter = resolve_runtime(
            attrs['provider_key'], attrs['runtime_type'])
        attrs['external_resource_id'] = validate_resource_id(
            adapter, attrs.get('external_resource_id', ''))
        attrs['_provider'] = provider
        attrs['_adapter'] = adapter
        return attrs


class ApplicationCreateSerializer(serializers.Serializer):
    """Payload of POST /api/v2/applications (新建智能体)."""

    name = serializers.CharField(max_length=100)
    slug = serializers.CharField(
        max_length=100, required=False, allow_blank=True, default='')
    description = serializers.CharField(
        required=False, allow_blank=True, default='')
    icon = serializers.CharField(
        max_length=50, required=False, allow_blank=True, default='')
    color = serializers.CharField(
        max_length=20, required=False, allow_blank=True, default='')
    category_slug = serializers.CharField(
        max_length=100, required=False, allow_blank=True, default='')
    category_name = serializers.CharField(
        max_length=100, required=False, allow_blank=True, default='')
    is_public = serializers.BooleanField(default=True)
    kind = serializers.ChoiceField(
        choices=[Application.Kind.CHAT], default=Application.Kind.CHAT)
    renderer_key = serializers.CharField(
        max_length=100, required=False, allow_blank=True, default='chat')
    runtime = RuntimeBindingInputSerializer(required=False)
    set_default_agent = serializers.BooleanField(default=False)

    def validate_name(self, value):
        value = value.strip()
        if not value:
            raise serializers.ValidationError('请输入智能体名称')
        return value

    def validate_slug(self, value):
        value = (value or '').strip().lower()
        if not value:
            return ''
        if not SLUG_PATTERN.match(value):
            raise serializers.ValidationError(
                '标识仅支持小写字母、数字和连字符')
        if Application.objects.filter(slug__iexact=value).exists():
            raise serializers.ValidationError('该标识已被占用')
        return value

    def validate_renderer_key(self, value):
        renderer = (value or '').strip() or 'chat'
        if renderer != 'chat':
            raise serializers.ValidationError(
                '智能体市场目前只创建 chat renderer 的应用')
        return renderer


class ApplicationUpdateSerializer(serializers.Serializer):
    """Payload of PATCH /api/v2/applications/{id}.

    Only the fields the marketplace edits; the runtime block updates the
    existing binding in place (an application has one binding per
    runtime/provider pair).
    """

    name = serializers.CharField(max_length=100, required=False)
    description = serializers.CharField(
        required=False, allow_blank=True)
    icon = serializers.CharField(
        max_length=50, required=False, allow_blank=True)
    color = serializers.CharField(
        max_length=20, required=False, allow_blank=True)
    is_public = serializers.BooleanField(required=False)
    category_slug = serializers.CharField(
        max_length=100, required=False, allow_blank=True)
    category_name = serializers.CharField(
        max_length=100, required=False, allow_blank=True)
    runtime = RuntimeBindingInputSerializer(required=False)
    set_default_agent = serializers.BooleanField(required=False)

    def validate_name(self, value):
        value = value.strip()
        if not value:
            raise serializers.ValidationError('请输入智能体名称')
        return value


# ---------------------------------------------------------------------------
# Views
# ---------------------------------------------------------------------------

def _caller_organization(request):
    from apps.enterprise.permissions import resolve_organization
    return resolve_organization(request, required=False)


def require_default_agent_eligible(application: Application) -> None:
    """Promote to workspace main agent, translating the domain error to 400."""
    try:
        Application.set_default_agent(application)
    except DjangoValidationError as exc:
        raise ValidationError({'detail': '；'.join(exc.messages)})


@transaction.atomic
def create_agent_application(request):
    """Create an Application (optionally with its runtime binding).

    Shared by ``POST /api/v2/applications``.
    """
    serializer = ApplicationCreateSerializer(data=request.data)
    serializer.is_valid(raise_exception=True)
    data = serializer.validated_data
    runtime = data.pop('runtime', None)
    set_default = data.pop('set_default_agent', False)
    category_name = data.pop('category_name', '')
    category_slug = data.pop('category_slug', '')

    slug = data.pop('slug', '') or f'agent-{uuid.uuid4().hex[:8]}'
    application = Application.objects.create(
        created_by=request.user,
        organization=_caller_organization(request),
        category=resolve_category(category_slug, category_name),
        slug=slug,
        **data,
    )

    if runtime is not None:
        provider = runtime['_provider']
        adapter = runtime['_adapter']
        ApplicationRuntimeBinding.objects.create(
            application=application,
            provider=provider,
            provider_key=provider.key,
            runtime_type=runtime['runtime_type'],
            external_resource_id=runtime['external_resource_id'],
            identity_mode=runtime['identity_mode'],
            execution_mode=runtime['execution_mode'],
            session_policy=runtime['session_policy'],
            artifact_policy=runtime['artifact_policy'],
            timeout_seconds=runtime['timeout_seconds'],
            config=runtime['config'],
            capabilities=getattr(adapter, 'capabilities', {}) or {},
            enabled=True,
        )
        logger.info(
            'application %s bound to %s:%s', application.slug,
            provider.key, runtime['external_resource_id'])

    if set_default:
        require_default_agent_eligible(application)
    return application


class ApplicationDetailView(APIView):
    """PATCH / DELETE 一个智能体应用（创建者或管理员）。"""

    permission_classes = [IsAuthenticated]

    def get(self, request, application_id):
        application = get_application(application_id)
        return Response(serialize_application(request, application))

    def patch(self, request, application_id):
        application = get_application(application_id)
        require_manage(request, application)
        serializer = ApplicationUpdateSerializer(data=request.data)
        serializer.is_valid(raise_exception=True)
        data = dict(serializer.validated_data)

        runtime = data.pop('runtime', None)
        set_default = data.pop('set_default_agent', None)
        category_name = data.pop('category_name', '')
        category_slug = data.pop('category_slug', '')

        with transaction.atomic():
            update_fields = []
            for field in ('name', 'description', 'icon', 'color', 'is_public'):
                if field in data:
                    setattr(application, field, data[field])
                    update_fields.append(field)
            if category_slug or category_name:
                application.category = resolve_category(
                    category_slug, category_name)
                update_fields.append('category')
            if update_fields:
                application.save(update_fields=update_fields + ['updated_at'])

            if runtime is not None:
                _upsert_binding(application, runtime)
            if set_default is True:
                require_default_agent_eligible(application)
            elif set_default is False and application.is_default_agent:
                Application.objects.filter(pk=application.pk).update(
                    is_default_agent=False)
        application.refresh_from_db()
        return Response(serialize_application(request, application))

    def delete(self, request, application_id):
        application = get_application(application_id)
        require_manage(request, application)
        stored_avatar = application.avatar.name if application.avatar else ''
        Application.objects.filter(pk=application.pk).update(
            is_default_agent=False)
        try:
            application.delete()
        except ProtectedError:
            # Conversation.application / Run.application are PROTECT: history
            # keeps the agent referenced (architecture doc §57).
            return Response(
                {'detail': '该智能体已有会话或运行记录，无法删除；'
                           '可先将其改为「不公开」。'},
                status=status.HTTP_409_CONFLICT)
        # The row (and its FileField) is gone, so drop the bytes too — a
        # deleted agent must not leave its avatar behind in MEDIA_ROOT.
        _drop_stored_file(stored_avatar)
        return Response(status=status.HTTP_204_NO_CONTENT)


def _upsert_binding(application: Application, runtime: dict) -> ApplicationRuntimeBinding:
    provider = runtime['_provider']
    adapter = runtime['_adapter']
    binding = application.runtime_bindings.filter(
        runtime_type=runtime['runtime_type'],
        provider_key=provider.key,
    ).first() or ApplicationRuntimeBinding(
        application=application,
        runtime_type=runtime['runtime_type'],
        provider_key=provider.key,
    )
    binding.provider = provider
    binding.external_resource_id = runtime['external_resource_id']
    binding.identity_mode = runtime['identity_mode']
    binding.execution_mode = runtime['execution_mode']
    binding.session_policy = runtime['session_policy']
    binding.artifact_policy = runtime['artifact_policy']
    binding.timeout_seconds = runtime['timeout_seconds']
    binding.config = runtime['config']
    binding.capabilities = getattr(adapter, 'capabilities', {}) or {}
    binding.enabled = True
    binding.save()
    return binding


class ApplicationAvatarView(APIView):
    """读取 / 修改头像（智能体市场「修改头像」）。

    GET is auth-checked and streamed from MEDIA_ROOT; POST accepts a single
    ``file`` multipart part (png/jpg/jpeg/gif/webp, ≤ 2MB); DELETE clears the
    field so the card falls back to the emoji ``icon``.
    """

    permission_classes = [IsAuthenticated]

    def get(self, request, application_id):
        application = get_application(application_id)
        if not application.avatar:
            raise NotFound('avatar not set')
        content_type = (
            mimetypes.guess_type(application.avatar.name)[0]
            or 'application/octet-stream')
        response = FileResponse(
            application.avatar.open('rb'), content_type=content_type)
        # `v=<updated_at>` busts the cache on replace; the file name already
        # changes, this just keeps proxies honest.
        response['Cache-Control'] = 'private, max-age=300'
        return response

    def post(self, request, application_id):
        application = get_application(application_id)
        require_manage(request, application)
        upload = request.FILES.get('file')
        if upload is None:
            raise ValidationError({'file': '请选择头像文件'})
        extension = (upload.name.rsplit('.', 1)[-1] or '').lower()
        if extension not in AVATAR_ALLOWED_EXTENSIONS:
            raise ValidationError({'file': (
                '头像仅支持 ' + ' / '.join(sorted(AVATAR_ALLOWED_EXTENSIONS))
                + ' 格式')})
        if upload.size > AVATAR_MAX_BYTES:
            raise ValidationError(
                {'file': f'头像不能超过 {AVATAR_MAX_BYTES // 1024 // 1024}MB'})

        previous = application.avatar.name if application.avatar else ''
        application.avatar.save(upload.name, upload, save=False)
        application.save(update_fields=['avatar', 'updated_at'])
        _drop_stored_file(previous)
        return Response(serialize_application(request, application))

    def delete(self, request, application_id):
        application = get_application(application_id)
        require_manage(request, application)
        previous = application.avatar.name if application.avatar else ''
        if not previous:
            return Response(serialize_application(request, application))
        application.avatar = ''
        application.save(update_fields=['avatar', 'updated_at'])
        _drop_stored_file(previous)
        return Response(serialize_application(request, application))


def _drop_stored_file(name: str) -> None:
    """Best-effort file cleanup: an orphaned avatar is not worth a 500."""
    if not name:
        return
    try:
        storage = Application._meta.get_field('avatar').storage
        storage.delete(name)
    except Exception as exc:  # noqa: BLE001
        logger.warning('failed to delete avatar %s: %s', name, exc)


class ApplicationDefaultAgentView(APIView):
    """POST / DELETE 工作台主智能体（架构文档 §38）。

    The main agent is the fallback target of the home composer and of an
    unresolvable ``@mention``; uniqueness is transactional because TiDB has no
    conditional unique constraints.
    """

    permission_classes = [IsAuthenticated]

    def post(self, request, application_id):
        application = get_application(application_id)
        require_manage(request, application)
        require_default_agent_eligible(application)
        application.refresh_from_db()
        return Response(serialize_application(request, application))

    def delete(self, request, application_id):
        application = get_application(application_id)
        require_manage(request, application)
        if application.is_default_agent:
            Application.objects.filter(pk=application.pk).update(
                is_default_agent=False)
        application.refresh_from_db()
        return Response(serialize_application(request, application))


class RuntimeCatalogView(APIView):
    """GET /api/v2/runtimes — 可用于新建智能体的运行时。

    Rendered straight from Provider rows × registered RuntimeAdapters, so the
    marketplace form stays provider-agnostic: adding a provider + adapter is
    enough for it to show up in 「新建智能体」.
    """

    permission_classes = [IsAuthenticated]

    def get(self, request):
        from apps.catalog.models import IdentityMode as _IdentityMode

        runtime_labels = dict(RuntimeType.choices)
        entries = []
        providers = Provider.objects.filter(
            status=Provider.Status.ACTIVE).order_by('key')
        for provider in providers:
            for runtime_type in (provider.supported_runtime_types or []):
                if not registry.has(provider.key, runtime_type):
                    continue
                adapter = registry.resolve(provider.key, runtime_type)
                label = getattr(adapter, 'display_label', '') or (
                    f'{provider.name} · {runtime_labels.get(runtime_type, runtime_type)}')
                entries.append({
                    'provider_key': provider.key,
                    'provider_name': provider.name,
                    'runtime_type': runtime_type,
                    'key': f'{provider.key}:{runtime_type}',
                    'label': label,
                    'resource_id_label': getattr(
                        adapter, 'resource_id_label', '') or '',
                    'resource_id_hint': getattr(
                        adapter, 'resource_id_hint', '') or '',
                    'resource_id_pattern': getattr(
                        adapter, 'resource_id_pattern', '') or '',
                    'resource_id_required': bool(
                        getattr(adapter, 'resource_id_label', '')),
                    'identity_modes': [choice[0] for choice in
                                       _IdentityMode.choices],
                    'execution_modes': [choice[0] for choice in
                                        ExecutionMode.choices],
                    'capabilities': getattr(adapter, 'capabilities', {}) or {},
                })
        return Response(entries)


class RuntimeValidationView(APIView):
    """POST /api/v2/runtimes/validate — 校验资源 ID 与可见性。

    Best-effort: providers without a visibility capability, or callers without
    the provider identity it needs, get ``checked=False`` plus a human message
    instead of an error — creating the agent stays possible offline.
    """

    permission_classes = [IsAuthenticated]

    def post(self, request):
        provider_key = (request.data.get('provider_key') or '').strip()
        runtime_type = (request.data.get('runtime_type') or '').strip()
        resource_id = (request.data.get('external_resource_id') or '').strip()
        identity_mode = (request.data.get('identity_mode') or
                         IdentityMode.USER).strip() or IdentityMode.USER

        _provider, adapter = resolve_runtime(provider_key, runtime_type)
        resource_id = validate_resource_id(adapter, resource_id)

        if not adapter.supports('visibility'):
            return Response({
                'ok': True, 'checked': False,
                'detail': '该运行时未声明可见性校验能力，已跳过远程校验。',
            })

        # Credential resolution and the visibility call are separated on
        # purpose: "the caller has no provider identity" means *we could not
        # check*, not *the agent is misconfigured*, so it degrades instead of
        # blocking the save.
        try:
            auth = asyncio.run(adapter.build_auth(
                request.user, identity_mode=identity_mode))
        except NotImplementedError:
            return Response({
                'ok': True, 'checked': False,
                'detail': '该运行时暂不支持身份解析，已跳过远程校验。',
            })
        except LookupError as exc:
            return Response({
                'ok': True, 'checked': False,
                'detail': _identity_hint(exc),
            })
        except Exception as exc:  # noqa: BLE001
            logger.info('runtime credential resolution failed: %s', exc)
            return Response({
                'ok': False, 'checked': True,
                'detail': f'无法获取访问凭据：{exc or exc.__class__.__name__}',
            })

        try:
            visible = asyncio.run(
                adapter.check_visibility(auth, resource_id))
        except Exception as exc:  # noqa: BLE001
            logger.info('runtime visibility check failed: %s', exc)
            return Response({
                'ok': False, 'checked': True,
                'detail': _friendly_validation_error(exc),
            })
        if visible:
            return Response({
                'ok': True, 'checked': True,
                'detail': '校验通过：当前身份对该智能体可见。',
            })
        return Response({
            'ok': False, 'checked': True,
            'detail': '当前身份对该智能体不可见，'
                      '请在飞书侧开启 OpenAPI 渠道并把使用者加入可访问范围。',
        })


def _identity_hint(exc: Exception) -> str:
    message = str(exc)
    if 'feishu identity' in message:
        return '当前账号未绑定飞书身份，无法做可见性校验；' \
               '请用飞书登录后再试，或直接保存后首次对话时验证。'
    return f'当前身份无法解析提供方凭据，已跳过远程校验：{message}'


def _friendly_validation_error(exc: Exception) -> str:
    message = str(exc)
    if 'user identity' in message:
        return '可见性校验需要用户身份（UAT）；请把「身份模式」设为 user 后重试。'
    if 'not enabled' in message or '10006' in message:
        return '该智能体未开启 OpenAPI 渠道，请在飞书智能体「使用渠道」中开启。'
    if '10007' in message or '10011' in message:
        return '当前用户不在该智能体的可见范围内，请在飞书侧调整可访问范围。'
    return f'校验未通过：{message or exc.__class__.__name__}'


urlpatterns = [
    path('applications/<int:application_id>',
         ApplicationDetailView.as_view(), name='v2-application-detail'),
    path('applications/<int:application_id>/avatar',
         ApplicationAvatarView.as_view(), name='v2-application-avatar'),
    path('applications/<int:application_id>/default-agent',
         ApplicationDefaultAgentView.as_view(),
         name='v2-application-default-agent'),
    path('runtimes', RuntimeCatalogView.as_view(), name='v2-runtimes'),
    path('runtimes/validate', RuntimeValidationView.as_view(),
         name='v2-runtime-validate'),
]
