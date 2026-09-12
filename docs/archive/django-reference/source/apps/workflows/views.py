from django.db import transaction
from django.db.models import Count, Q
from django.utils import timezone
from rest_framework import status, viewsets
from rest_framework.decorators import action
from rest_framework.permissions import IsAuthenticated
from rest_framework.response import Response

from apps.enterprise.permissions import resolve_organization
from apps.projects.models import Project
from apps.projects.services.workspace_paths import workflow_working_directories
from .models import Workflow, WorkflowRun, WorkflowStepRun
from .serializers import (
    SelectStepSerializer,
    WorkflowDetailSerializer,
    WorkflowListSerializer,
    WorkflowRunSerializer,
    WorkflowWriteSerializer,
)


class WorkflowViewSet(viewsets.ModelViewSet):
    permission_classes = [IsAuthenticated]

    def get_queryset(self):
        organization = resolve_organization(self.request)
        return Workflow.objects.filter(
            organization=organization,
        ).filter(Q(owner=self.request.user) | Q(is_public=True)).annotate(
            step_count=Count('steps')).prefetch_related(
            'steps__application__chat_profile',
            'steps__application__agent_bindings__agent',
            'steps__application__skill_bindings__skill',
            'steps__application__guided_prompts__questions__options',
        )

    def get_serializer_class(self):
        if self.action in ('create', 'update', 'partial_update'):
            return WorkflowWriteSerializer
        if self.action == 'list':
            return WorkflowListSerializer
        return WorkflowDetailSerializer

    def perform_create(self, serializer):
        organization = resolve_organization(self.request)
        serializer.save(owner=self.request.user, organization=organization)

    def perform_update(self, serializer):
        if serializer.instance.owner_id != self.request.user.id:
            from rest_framework.exceptions import PermissionDenied
            raise PermissionDenied('只有工作流所有者可以编辑。')
        serializer.save()

    @action(detail=True, methods=['post'])
    def start(self, request, pk=None):
        workflow = self.get_object()
        steps = list(workflow.steps.select_related('application').order_by('order'))
        if not steps:
            return Response({'detail': '工作流至少需要一个应用。'}, status=400)
        with transaction.atomic():
            project = Project.objects.create(
                user=request.user,
                organization=workflow.organization,
                title=f'{workflow.name} 工作区',
                description=workflow.description,
                structure={'workflow_id': str(workflow.id)},
                status='active',
            )
            run = WorkflowRun.objects.create(
                workflow=workflow,
                project=project,
                organization=workflow.organization,
                started_by=request.user,
                selected_step=steps[0],
            )
            WorkflowStepRun.objects.bulk_create([
                WorkflowStepRun(
                    workflow_run=run,
                    workflow_step=step,
                    source_step_id=step.id,
                    application=step.application,
                    name=step.name,
                    config=step.config,
                    order=step.order,
                    status=(WorkflowStepRun.Status.ACTIVE
                            if step.id == steps[0].id else WorkflowStepRun.Status.IDLE),
                    last_opened_at=(timezone.now() if step.id == steps[0].id else None),
                ) for step in steps
            ])
            run.selected_step_run = run.step_runs.get(source_step_id=steps[0].id)
            run.save(update_fields=['selected_step_run', 'updated_at'])
            workflow_working_directories(run)
        run = self._run_queryset().get(id=run.id)
        return Response(WorkflowRunSerializer(run).data,
                        status=status.HTTP_201_CREATED)

    @staticmethod
    def _run_queryset():
        return WorkflowRun.objects.select_related(
            'workflow', 'project', 'selected_step',
            'selected_step_run').prefetch_related(
            'step_runs__application__chat_profile',
            'step_runs__application__agent_bindings__agent',
            'step_runs__application__skill_bindings__skill',
            'step_runs__application__guided_prompts__questions__options',
        )


class WorkflowRunViewSet(viewsets.ReadOnlyModelViewSet):
    permission_classes = [IsAuthenticated]
    serializer_class = WorkflowRunSerializer

    def get_queryset(self):
        organization = resolve_organization(self.request)
        return WorkflowViewSet._run_queryset().filter(
            organization=organization, started_by=self.request.user)

    @action(detail=True, methods=['post'], url_path='select-step')
    def select_step(self, request, pk=None):
        run = self.get_object()
        serializer = SelectStepSerializer(data=request.data)
        serializer.is_valid(raise_exception=True)
        try:
            selected = run.step_runs.select_related('workflow_step').get(
                source_step_id=serializer.validated_data['step_id'])
        except WorkflowStepRun.DoesNotExist:
            return Response({'detail': '该应用不属于当前工作流。'}, status=404)
        now = timezone.now()
        run.step_runs.filter(status=WorkflowStepRun.Status.ACTIVE).update(
            status=WorkflowStepRun.Status.IDLE)
        if selected.status != WorkflowStepRun.Status.COMPLETED:
            selected.status = WorkflowStepRun.Status.ACTIVE
        selected.last_opened_at = now
        selected.save(update_fields=['status', 'last_opened_at'])
        run.selected_step = selected.workflow_step
        run.selected_step_run = selected
        run.save(update_fields=['selected_step', 'selected_step_run', 'updated_at'])
        run = WorkflowViewSet._run_queryset().get(id=run.id)
        return Response(WorkflowRunSerializer(run).data)

    @action(detail=True, methods=['post'], url_path='complete-step')
    def complete_step(self, request, pk=None):
        run = self.get_object()
        serializer = SelectStepSerializer(data=request.data)
        serializer.is_valid(raise_exception=True)
        updated = run.step_runs.filter(
            source_step_id=serializer.validated_data['step_id']).update(
                status=WorkflowStepRun.Status.COMPLETED,
                completed_at=timezone.now())
        if not updated:
            return Response({'detail': '该应用不属于当前工作流。'}, status=404)
        if not run.step_runs.exclude(
                status=WorkflowStepRun.Status.COMPLETED).exists():
            run.status = WorkflowRun.Status.COMPLETED
            run.save(update_fields=['status', 'updated_at'])
        run = WorkflowViewSet._run_queryset().get(id=run.id)
        return Response(WorkflowRunSerializer(run).data)
