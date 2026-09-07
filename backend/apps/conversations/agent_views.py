"""Read and control the Codex-shaped Agent API v2 resources."""
from django.db import transaction
from django.db.models import Prefetch, Q
from django.shortcuts import get_object_or_404
from django.utils import timezone
from rest_framework import mixins, status, viewsets
from rest_framework.decorators import action
from rest_framework.permissions import IsAuthenticated
from rest_framework.response import Response

from apps.enterprise.permissions import resolve_organization
from core.agent_engine.runtime import session_registry

from .models import AgentItem, AgentServerRequest, AgentThread, AgentTurn
from .serializers import (
    AgentServerRequestProtocolSerializer,
    AgentThreadProtocolSerializer,
    AgentTurnProtocolSerializer,
    ResumeAgentSerializer,
)


def _visible_threads(request):
    organization = resolve_organization(request)
    return AgentThread.objects.filter(
        conversation__user=request.user,
    ).filter(
        Q(conversation__organization=organization)
        | Q(conversation__organization__isnull=True)
    )


class AgentThreadViewSet(viewsets.ReadOnlyModelViewSet):
    """List and inspect canonical threads and their materialized items."""

    permission_classes = [IsAuthenticated]
    serializer_class = AgentThreadProtocolSerializer

    def get_queryset(self):
        items = AgentItem.objects.order_by('ordinal', 'created_at')
        turns = AgentTurn.objects.prefetch_related(
            Prefetch('items', queryset=items)
        ).order_by('started_at')
        return _visible_threads(self.request).select_related(
            'conversation'
        ).prefetch_related(Prefetch('turns', queryset=turns)).order_by(
            '-updated_at'
        )

    @action(detail=True, methods=['get'])
    def turns(self, request, pk=None):
        thread = self.get_object()
        queryset = thread.turns.prefetch_related('items').order_by('started_at')
        return Response(AgentTurnProtocolSerializer(queryset, many=True).data)


class AgentTurnViewSet(viewsets.ReadOnlyModelViewSet):
    """Inspect a canonical turn independently from its thread."""

    permission_classes = [IsAuthenticated]
    serializer_class = AgentTurnProtocolSerializer

    def get_queryset(self):
        return AgentTurn.objects.filter(
            thread__in=_visible_threads(self.request)
        ).select_related('thread').prefetch_related('items')


class AgentServerRequestViewSet(
    mixins.ListModelMixin,
    mixins.RetrieveModelMixin,
    viewsets.GenericViewSet,
):
    """Resolve approval and request-user-input messages by request id."""

    permission_classes = [IsAuthenticated]
    serializer_class = AgentServerRequestProtocolSerializer

    def get_queryset(self):
        return AgentServerRequest.objects.filter(
            thread__in=_visible_threads(self.request)
        ).select_related('thread__conversation', 'turn')

    @action(detail=True, methods=['post'])
    @transaction.atomic
    def resolve(self, request, pk=None):
        server_request = get_object_or_404(
            self.get_queryset().select_for_update(), pk=pk)
        if server_request.status != AgentServerRequest.Status.PENDING:
            return Response(
                {'detail': 'Server request has already been resolved.'},
                status=status.HTTP_409_CONFLICT,
            )
        serializer = ResumeAgentSerializer(data=request.data)
        serializer.is_valid(raise_exception=True)
        session = session_registry.get(
            str(server_request.thread.conversation_id)
        )
        if session is None:
            return Response(
                {'detail': 'The agent turn is no longer active.'},
                status=status.HTTP_409_CONFLICT,
            )
        result = session.resume(
            text=serializer.validated_data['text'],
            selections=serializer.validated_data['selections'],
        )
        server_request.status = AgentServerRequest.Status.RESOLVED
        server_request.response = serializer.validated_data
        server_request.resolved_at = timezone.now()
        server_request.save(update_fields=['status', 'response', 'resolved_at'])
        return Response({
            'status': getattr(result, 'value', str(result)),
            'request': self.get_serializer(server_request).data,
        })
