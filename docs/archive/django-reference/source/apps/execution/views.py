"""v2 execution API.

POST   /api/v2/runs                     create a run (fast — web only queues)
GET    /api/v2/runs/{id}                 run status
GET    /api/v2/runs/{id}/events          persisted events (reconnect replay)
GET    /api/v2/runs/{id}/stream          live SSE (Redis fanout)
POST   /api/v2/runs/{id}/commands       cancel / respond / resume
GET    /api/v2/runs/{id}/artifacts      artifacts of a run
GET    /api/v2/artifacts/{id}/open      resolve + 302 to provider URL
POST   /api/v2/runs/{id}/attachments    upload a provider attachment
GET    /api/v2/conversations/{id}/runs   runs of a conversation
GET    /api/v2/applications              openable applications (+favorite/usage)
POST   /api/v2/applications              create an application (智能体市场)
POST   /api/v2/applications/{id}/favorite  pin an application
DELETE /api/v2/applications/{id}/favorite  unpin an application

The rest of the application authoring API (edit / delete / avatar /
default-agent / runtime catalog) lives in apps/applications/v2_authoring.py;
apps/execution/urls.py mounts both modules under /api/v2/.
"""
from __future__ import annotations

import json
import logging

from asgiref.sync import async_to_sync

from django.http import StreamingHttpResponse
from django.urls import path
from rest_framework import status
from rest_framework.exceptions import NotFound, ValidationError
from rest_framework.permissions import IsAuthenticated
from rest_framework.response import Response
from rest_framework.views import APIView

from apps.catalog.runtime import registry
from apps.conversations.models import Conversation
from apps.execution.artifact_resolver import (
    ArtifactResolutionError,
    resolve_artifact_url,
)
from apps.execution.models import (
    Run,
    RunArtifact,
    RunCommand,
    RuntimeAttachment,
)
from apps.execution.pubsub import run_channel
from apps.execution.serializers import (
    RunArtifactSerializer,
    RunCommandSerializer,
    RunEventSerializer,
    RunSerializer,
    RuntimeAttachmentSerializer,
)
from apps.execution.services import RunManager
from apps.identity.views import _json_error

logger = logging.getLogger(__name__)


def _get_owned_run(request, run_id) -> Run:
    run = Run.objects.filter(pk=run_id).first()
    if run is None:
        raise NotFound('run not found')
    if run.user_id and run.user_id != request.user.pk and not request.user.is_staff:
        raise NotFound('run not found')
    return run


# ---------------------------------------------------------------------------
# Run creation (web process only creates + queues)
# ---------------------------------------------------------------------------

class RunCreateView(APIView):
    permission_classes = [IsAuthenticated]

    def post(self, request):
        application_id = request.data.get('application_id')
        conversation_id = request.data.get('conversation_id')
        content = request.data.get('content')
        attachment_ids = request.data.get('attachment_ids') or []

        if not application_id:
            raise ValidationError('application_id is required')
        if not content:
            raise ValidationError('content is required')

        from apps.applications.models import Application
        application = Application.objects.filter(
            pk=application_id).first()
        if application is None:
            raise ValidationError('application not found')

        binding = application.runtime_bindings.filter(
            enabled=True).order_by('-created_at').first()
        if binding is None:
            raise ValidationError('application has no runtime binding')

        # Lazy conversation: the idle workspace creates nothing; sending the
        # first message creates the local Conversation, but the Aily Session
        # remains lazy until the worker starts the first chat.
        conversation = None
        if conversation_id:
            conversation = Conversation.objects.filter(
                pk=conversation_id, user=request.user,
                application=application).first()
            if conversation is None:
                raise ValidationError('conversation not found')
        else:
            conversation = Conversation.objects.create(
                user=request.user,
                organization=getattr(request, 'organization', None),
                application=application,
                title=str(content)[:80],
            )

        from apps.execution.models import RuntimeAttachment
        attachments = list(RuntimeAttachment.objects.filter(
            id__in=attachment_ids,
            created_by=request.user,
            provider=binding.provider_key,
            status=RuntimeAttachment.Status.UPLOADED,
            run__isnull=True,
        ))
        if len(attachments) != len(set(map(str, attachment_ids))):
            raise ValidationError('one or more attachments are invalid')
        external_attachment_ids = [
            item.external_attachment_id for item in attachments]

        run = RunManager.create_run(
            user=request.user,
            organization=getattr(request, 'organization', None),
            application=application,
            conversation=conversation,
            runtime_binding=binding,
            input={
                'content': [{'type': 'text', 'text': content}],
                'agent_attachment_ids': external_attachment_ids,
            },
            execution_mode=(
                binding.execution_mode
                if binding.execution_mode else 'interactive'),
        )
        if attachments:
            RuntimeAttachment.objects.filter(
                id__in=[item.pk for item in attachments]).update(run=run)
        conversation.messages.create(
            role='user', content=content,
            metadata={
                'run_id': str(run.pk),
                'attachment_ids': [str(item.pk) for item in attachments],
            })
        return Response(RunSerializer(run).data,
                        status=status.HTTP_201_CREATED)


class RunDetailView(APIView):
    permission_classes = [IsAuthenticated]

    def get(self, request, run_id):
        run = _get_owned_run(request, run_id)
        return Response(RunSerializer(run).data)


class RunEventsView(APIView):
    permission_classes = [IsAuthenticated]

    def get(self, request, run_id):
        run = _get_owned_run(request, run_id)
        after = int(request.query_params.get('after', 0) or 0)
        events = run.events.filter(sequence__gt=after).order_by('sequence')
        return Response(RunEventSerializer(events, many=True).data)


class RunStreamView(APIView):
    """SSE gateway: live events from Redis, replayed from TiDB first."""

    permission_classes = [IsAuthenticated]

    def get(self, request, run_id):
        from django_redis import get_redis_connection

        run = _get_owned_run(request, run_id)
        channel = run_channel(run.pk)
        replay = list(run.events.order_by('sequence').values(
            'sequence', 'event_type', 'payload'))
        terminal = run.is_terminal

        def event_stream():
            for event in replay:
                yield self._sse('run.event', {
                    'run_id': str(run.pk),
                    **event,
                })
            if terminal:
                return

            redis = get_redis_connection('default')
            pubsub = redis.pubsub(ignore_subscribe_messages=True)
            pubsub.subscribe(channel)
            try:
                while True:
                    message = pubsub.get_message(timeout=15)
                    if message is None:
                        yield ': keepalive\n\n'
                        continue
                    data = message.get('data')
                    if isinstance(data, bytes):
                        data = data.decode()
                    payload = json.loads(data)
                    yield self._sse('run.event', payload)
                    if payload.get('event_type') in {
                        'run.completed', 'run.failed', 'run.interrupted'}:
                        return
            finally:
                pubsub.close()

        resp = StreamingHttpResponse(
            event_stream(), content_type='text/event-stream')
        resp['Cache-Control'] = 'no-cache, no-transform'
        resp['X-Accel-Buffering'] = 'no'
        return resp

    @staticmethod
    def _sse(event_type: str, payload: dict) -> str:
        return f'event: {event_type}\ndata: {json.dumps(payload, ensure_ascii=False)}\n\n'


class RunCommandsView(APIView):
    permission_classes = [IsAuthenticated]

    def post(self, request, run_id):
        run = _get_owned_run(request, run_id)
        serializer = RunCommandSerializer(data=request.data)
        serializer.is_valid(raise_exception=True)
        command_type = serializer.validated_data['command_type']
        if command_type == RunCommand.Type.CANCEL:
            adapter = registry.resolve(run.provider, run.runtime_type)
            if not adapter.supports('cancel'):
                return Response(
                    {'error': 'provider does not support cancel'},
                    status=status.HTTP_409_CONFLICT)
        command = RunCommand.objects.create(
            run=run, command_type=command_type,
            payload=serializer.validated_data.get('payload') or {},
            created_by=request.user)
        return Response(RunCommandSerializer(command).data, status=201)


class RunArtifactsView(APIView):
    permission_classes = [IsAuthenticated]

    def get(self, request, run_id):
        run = _get_owned_run(request, run_id)
        artifacts = run.artifacts.order_by('created_at')
        return Response(RunArtifactSerializer(artifacts, many=True).data)


class ArtifactOpenView(APIView):
    """Resolve then 302 to the provider's (24h) download URL."""

    permission_classes = [IsAuthenticated]

    def get(self, request, artifact_id):
        artifact = RunArtifact.objects.select_related(
            'run__runtime_binding', 'run__user').filter(pk=artifact_id).first()
        if artifact is None:
            raise NotFound('artifact not found')
        run = artifact.run
        if run.user_id and run.user_id != request.user.pk \
                and not request.user.is_staff:
            raise NotFound('artifact not found')
        try:
            # A dedicated event loop per request: async_to_sync in a Django
            # dev-server thread can hit "Event loop is closed" on Windows
            # when a previous loop on this thread was torn down.
            import asyncio
            url = asyncio.run(resolve_artifact_url(artifact, request.user))
        except ArtifactResolutionError as exc:
            return _json_error(str(exc), 502)
        from django.shortcuts import redirect
        return redirect(url)


class ApplicationAttachmentsView(APIView):
    """Upload an attachment before creating a Run.

    The returned local attachment id is passed to POST /api/v2/runs. The
    server then validates ownership and converts it to the provider's
    external attachment id, preventing users from injecting another user's
    Aily attachment id.
    """

    permission_classes = [IsAuthenticated]

    def post(self, request, application_id):
        from apps.applications.models import Application
        application = Application.objects.filter(pk=application_id).first()
        if application is None:
            raise NotFound('application not found')
        binding = application.runtime_bindings.filter(
            enabled=True).order_by('-created_at').first()
        if binding is None:
            raise ValidationError('application has no runtime binding')

        upload = request.FILES.get('file')
        attachment_type = request.data.get('type', 'file')
        doc_url = request.data.get('doc_url', '')
        if not upload and not doc_url:
            raise ValidationError('file or doc_url is required')

        attachment = RuntimeAttachment.objects.create(
            provider=binding.provider_key,
            attachment_type=attachment_type,
            name=upload.name if upload else doc_url.rsplit('/', 1)[-1],
            source_type=('doc_url' if doc_url else 'upload'),
            source_url=doc_url,
            auth_mode=binding.identity_mode,
            auth_subject_key=str(request.user.pk),
            created_by=request.user,
        )
        try:
            from integrations.aily import auth as aily_auth
            auth = async_to_sync(aily_auth.build_auth_context)(
                request.user, identity_mode=binding.identity_mode)
            adapter = registry.resolve(
                binding.provider_key, binding.runtime_type)
            external_id = async_to_sync(adapter.upload_attachment)(
                auth,
                binding.external_resource_id,
                file_bytes=upload.read() if upload else None,
                filename=upload.name if upload else '',
                attachment_type=attachment_type,
                doc_url=doc_url,
            )
        except Exception as exc:  # noqa: BLE001
            attachment.status = RuntimeAttachment.Status.FAILED
            attachment.save(update_fields=['status', 'updated_at'])
            logger.warning('provider attachment upload failed: %s', exc)
            return Response(
                {'error': 'attachment upload failed'},
                status=status.HTTP_502_BAD_GATEWAY)

        attachment.external_attachment_id = external_id
        attachment.status = RuntimeAttachment.Status.UPLOADED
        attachment.save(update_fields=[
            'external_attachment_id', 'status', 'updated_at'])
        return Response(
            RuntimeAttachmentSerializer(attachment).data,
            status=status.HTTP_201_CREATED)


class RunAttachmentsView(APIView):
    """Upload an attachment to the provider under the caller's identity."""

    permission_classes = [IsAuthenticated]

    def post(self, request, run_id):
        run = _get_owned_run(request, run_id)
        if run.status != Run.Status.QUEUED:
            return Response(
                {'error': 'attachments can only be added while run is queued'},
                status=status.HTTP_409_CONFLICT)
        upload = request.FILES.get('file')
        attachment_type = request.POST.get('type', 'file')
        doc_url = request.POST.get('doc_url', '')

        if not upload and not doc_url:
            raise ValidationError('file or doc_url is required')

        snapshot = run.runtime_snapshot or {}
        agent_id = snapshot.get('external_resource_id') or (
            run.runtime_binding.external_resource_id
            if run.runtime_binding else '')
        if not agent_id:
            raise ValidationError('run has no external_resource_id')

        attachment = RuntimeAttachment.objects.create(
            run=run, conversation=run.conversation,
            provider=run.provider,
            attachment_type=attachment_type,
            name=upload.name if upload else (doc_url.rsplit('/', 1)[-1]),
            source_type=('doc_url' if doc_url else 'upload'),
            source_url=doc_url,
            auth_mode=snapshot.get('identity_mode', 'user'),
            auth_subject_key=str(run.user_id or ''),
            created_by=request.user,
        )
        try:
            from integrations.aily import auth as aily_auth
            auth = async_to_sync(aily_auth.build_auth_context)(
                request.user,
                identity_mode=snapshot.get('identity_mode', 'user'))
            adapter = registry.resolve(run.provider, run.runtime_type)
            file_bytes = upload.read() if upload else None
            external_id = async_to_sync(adapter.upload_attachment)(
                auth, agent_id,
                file_bytes=file_bytes,
                filename=upload.name if upload else '',
                attachment_type=attachment_type,
                doc_url=doc_url)
        except Exception as exc:  # noqa: BLE001
            attachment.status = RuntimeAttachment.Status.FAILED
            attachment.save(update_fields=['status'])
            return _json_error(f'attachment upload failed: {exc}', 502)

        attachment.external_attachment_id = external_id
        attachment.status = RuntimeAttachment.Status.UPLOADED
        attachment.save(update_fields=[
            'external_attachment_id', 'status', 'updated_at'])
        input_payload = dict(run.input or {})
        external_ids = list(input_payload.get('agent_attachment_ids') or [])
        external_ids.append(external_id)
        input_payload['agent_attachment_ids'] = external_ids
        # This guarded update closes the race with the worker's queued->running
        # claim: if it has already started, the late attachment is rejected.
        updated = Run.objects.filter(
            pk=run.pk, status=Run.Status.QUEUED).update(input=input_payload)
        if not updated:
            attachment.delete()
            return Response(
                {'error': 'run started before attachment could be bound'},
                status=status.HTTP_409_CONFLICT)
        return Response(
            RuntimeAttachmentSerializer(attachment).data, status=201)


class ConversationRunsView(APIView):
    permission_classes = [IsAuthenticated]

    def get(self, request, conversation_id):
        conversation = Conversation.objects.filter(
            pk=conversation_id, user=request.user).first()
        if conversation is None:
            raise NotFound('conversation not found')
        runs = conversation.runs.order_by('-created_at')
        return Response(RunSerializer(runs, many=True).data)


def _capabilities_for(binding) -> dict:
    """Binding-level capability declaration wins over provider defaults."""
    if binding is None:
        return {}
    if binding.capabilities:
        return binding.capabilities
    if binding.provider:
        return binding.provider.capabilities or {}
    return {}


class ApplicationsIndexView(APIView):
    """Applications the caller can open from the workspace.

    Provider-agnostic: only applications with an enabled runtime binding
    are listed, and the capability flags come from the binding so the UI can
    decide what to render (attachments, streaming, cancel...).

    Workspace home (§35) also needs the non-chat entries (fixed pages /
    tasks) and the caller's own favourites + usage so it can build the
    常用/最近使用/收藏 shortcuts. ``kind`` filters the listing
    (``chat`` — the default, so the chat composer's default-app resolver is
    unaffected — ``task``, ``custom`` or ``all``).

    ``scope`` widens *who* is listed: ``public`` (default, unchanged) →
    public applications only; ``manage`` → public + the caller's own, private
    included, so a user's own agents show up in the workspace switcher and in
    智能体市场; ``mine`` → the caller's own only.

    ``include_unbound`` additionally returns chat applications *without* an
    enabled binding (bounded by ``scope``). The marketplace needs them so an
    agent whose binding was removed stays visible and repairable; the
    workspace omits the flag because an unbound chat app cannot start a Run.

    ``POST`` creates an application (新建智能体) via
    apps.applications.v2_authoring, which owns catalog authoring.
    """

    permission_classes = [IsAuthenticated]

    def post(self, request):
        from apps.applications.v2_authoring import (
            create_agent_application,
            serialize_application,
        )
        application = create_agent_application(request)
        return Response(
            serialize_application(request, application),
            status=status.HTTP_201_CREATED)

    def get(self, request):
        from django.db.models import Count, Max, Q

        from apps.applications.models import (
            Application,
            ApplicationCategory,
            ApplicationFavorite,
        )
        from apps.applications.v2_authoring import avatar_url, can_manage
        from apps.catalog.models import ApplicationRuntimeBinding

        requested_kind = (request.query_params.get('kind') or 'chat').strip()
        if requested_kind not in {'chat', 'task', 'custom', 'all'}:
            requested_kind = 'chat'

        requested_scope = (request.query_params.get('scope') or 'public').strip()
        if requested_scope not in {'public', 'mine', 'manage'}:
            requested_scope = 'public'
        include_unbound = (request.query_params.get('include_unbound') or ''
                           ).strip().lower() in {'1', 'true', 'yes'}

        def visibility(prefix: str = '') -> Q:
            """Which applications the caller may see, as a prefixed Q."""
            mine = Q(**{f'{prefix}created_by': request.user})
            if requested_scope == 'mine':
                return mine
            public = Q(**{f'{prefix}is_public': True})
            return public | mine if requested_scope == 'manage' else public

        bindings = ApplicationRuntimeBinding.objects.filter(
            enabled=True).filter(visibility('application__'))
        if requested_kind != 'all':
            bindings = bindings.filter(application__kind=requested_kind)
        else:
            # `all` still restricts chat applications to bound ones: an
            # unbound chat app cannot start a Run, so it is not openable.
            bindings = bindings.filter(application__kind=Application.Kind.CHAT)

        latest_by_application: dict[int, ApplicationRuntimeBinding] = {}
        for binding in bindings.select_related(
                'application', 'provider').order_by('application__created_at'):
            latest_by_application.setdefault(binding.application_id, binding)

        applications: list[Application] = [
            binding.application for binding in latest_by_application.values()]

        bound_ids = list(latest_by_application.keys())
        if requested_kind == 'all':
            # Fixed pages have no runtime binding; they are opened by the
            # renderer registry instead of a provider run. Unbound chat apps
            # only appear when the caller asks for them (marketplace).
            extras = Application.objects.filter(
                visibility()).exclude(id__in=bound_ids)
            if not include_unbound:
                extras = extras.exclude(kind=Application.Kind.CHAT)
        elif requested_kind == 'chat':
            extras = Application.objects.none()
            if include_unbound:
                extras = Application.objects.filter(
                    visibility(), kind=Application.Kind.CHAT,
                ).exclude(id__in=bound_ids)
        else:
            extras = Application.objects.filter(
                visibility(), kind=requested_kind).exclude(id__in=bound_ids)
        applications.extend(extras.order_by('created_at'))

        application_ids = [app.pk for app in applications]

        categories = {
            category.pk: category for category in
            ApplicationCategory.objects.filter(
                pk__in={app.category_id for app in applications})
        }

        favorite_ids = set(ApplicationFavorite.objects.filter(
            user=request.user,
            application_id__in=application_ids,
        ).values_list('application_id', flat=True))

        stats = {
            row['application_id']: row
            for row in Run.objects.filter(
                user=request.user,
                application_id__in=application_ids,
            ).values('application_id').annotate(
                total=Count('id'), last=Max('created_at'))
        }

        data = []
        for application in applications:
            binding = latest_by_application.get(application.pk)
            usage = stats.get(application.pk) or {}
            category = categories.get(application.category_id)
            data.append({
                'id': application.pk,
                'slug': application.slug,
                'name': application.name,
                'description': application.description,
                'icon': application.icon,
                'avatar_url': avatar_url(application),
                'color': application.color,
                'kind': application.kind,
                'renderer_key': application.renderer_key,
                'executor_key': application.executor_key,
                'category_slug': category.slug if category else '',
                'category_name': category.name if category else '',
                'is_public': application.is_public,
                'runtime_type': binding.runtime_type if binding else '',
                'provider_key': binding.provider_key if binding else '',
                'external_resource_id': (
                    binding.external_resource_id if binding else ''),
                'identity_mode': binding.identity_mode if binding else '',
                'execution_mode': binding.execution_mode if binding else '',
                # Binding-level capabilities override the provider defaults.
                'capabilities': _capabilities_for(binding),
                'is_bound': binding is not None,
                'is_favorite': application.pk in favorite_ids,
                # The workspace's explicit main agent (§38); the frontend falls
                # back to the first bound chat app when none is marked.
                'is_default_agent': application.is_default_agent,
                'can_manage': can_manage(request, application),
                # Personal usage drives 常用/最近使用 ordering; the global
                # counter (Application.usage_count) drives 推荐 only.
                'usage_count': usage.get('total', 0),
                'last_used_at': usage.get('last'),
                'global_usage_count': application.usage_count,
            })
        return Response(data)


class ApplicationFavoriteView(APIView):
    """Pin / unpin an application for the calling user (§35 收藏).

    Both verbs are idempotent so a double click cannot 500 the workspace.
    """

    permission_classes = [IsAuthenticated]

    def _application(self, application_id):
        from apps.applications.models import Application
        application = Application.objects.filter(
            pk=application_id).first()
        if application is None:
            raise NotFound('application not found')
        return application

    def post(self, request, application_id):
        from apps.applications.models import ApplicationFavorite
        application = self._application(application_id)
        ApplicationFavorite.objects.get_or_create(
            user=request.user, application=application)
        return Response({'application_id': application.pk, 'is_favorite': True})

    def delete(self, request, application_id):
        from apps.applications.models import ApplicationFavorite
        application = self._application(application_id)
        ApplicationFavorite.objects.filter(
            user=request.user, application=application).delete()
        return Response({'application_id': application.pk, 'is_favorite': False})



urlpatterns = [
    path('runs', RunCreateView.as_view(), name='v2-run-create'),
    path('runs/<uuid:run_id>', RunDetailView.as_view(), name='v2-run-detail'),
    path('runs/<uuid:run_id>/events', RunEventsView.as_view(),
         name='v2-run-events'),
    path('runs/<uuid:run_id>/stream', RunStreamView.as_view(),
         name='v2-run-stream'),
    path('runs/<uuid:run_id>/commands', RunCommandsView.as_view(),
         name='v2-run-commands'),
    path('runs/<uuid:run_id>/artifacts', RunArtifactsView.as_view(),
         name='v2-run-artifacts'),
    path('applications/<int:application_id>/attachments',
         ApplicationAttachmentsView.as_view(),
         name='v2-application-attachments'),
    path('runs/<uuid:run_id>/attachments', RunAttachmentsView.as_view(),
         name='v2-run-attachments'),
    path('artifacts/<uuid:artifact_id>/open', ArtifactOpenView.as_view(),
         name='v2-artifact-open'),
    path('conversations/<int:conversation_id>/runs',
         ConversationRunsView.as_view(), name='v2-conversation-runs'),
    path('applications', ApplicationsIndexView.as_view(),
         name='v2-applications'),
    path('applications/<int:application_id>/favorite',
         ApplicationFavoriteView.as_view(), name='v2-application-favorite'),
]
