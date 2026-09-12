"""REST views for app_runner: create/get/stop jobs and folder scanning."""
import os
import string
from pathlib import Path

from django.conf import settings
from rest_framework import status
from rest_framework.decorators import api_view, permission_classes
from rest_framework.permissions import IsAuthenticated
from rest_framework.response import Response

from .executors import VIDEO_EXTENSIONS
from .job_manager import job_manager
from .models import Job
from apps.enterprise.permissions import resolve_organization
from .serializers import CreateJobSerializer, JobSerializer, ScanFolderSerializer


@api_view(['POST'])
@permission_classes([IsAuthenticated])
def create_job(request):
    """Create a Job and start its executor. Returns the job id."""
    ser = CreateJobSerializer(data=request.data)
    ser.is_valid(raise_exception=True)
    app_slug = ser.validated_data['app_slug'].slug
    config = ser.validated_data['config']

    job = Job.objects.create(app_slug=app_slug, config=config, owner=request.user,
                             organization=resolve_organization(request),
                             status=Job.Status.PENDING)
    if settings.APP_RUNNER_INLINE_EXECUTION:
        job_manager.start(job)
    return Response(JobSerializer(job).data, status=status.HTTP_201_CREATED)


@api_view(['GET'])
@permission_classes([IsAuthenticated])
def job_detail(request, id):
    try:
        job = Job.objects.get(id=id, owner=request.user)
    except Job.DoesNotExist:
        return Response({'detail': '未找到该任务'}, status=status.HTTP_404_NOT_FOUND)
    return Response(JobSerializer(job).data)


@api_view(['POST'])
@permission_classes([IsAuthenticated])
def stop_job(request, id):
    try:
        Job.objects.get(id=id, owner=request.user)
    except Job.DoesNotExist:
        return Response({'detail': '未找到该任务'}, status=status.HTTP_404_NOT_FOUND)
    job_manager.stop(str(id))
    return Response(status=status.HTTP_204_NO_CONTENT)


@api_view(['POST'])
@permission_classes([IsAuthenticated])
def scan_folder(request):
    ser = ScanFolderSerializer(data=request.data)
    ser.is_valid(raise_exception=True)
    try:
        path = _resolve_allowed_path(ser.validated_data['path'])
    except PermissionError as exc:
        return Response({'detail': str(exc)}, status=status.HTTP_403_FORBIDDEN)
    videos = []
    if os.path.isdir(path):
        for f in sorted(os.listdir(path)):
            if os.path.splitext(f)[1].lower() in VIDEO_EXTENSIONS:
                videos.append({'name': f, 'path': os.path.join(path, f)})
    return Response({'folder': path, 'videos': videos})


def _list_roots():
    """Only expose explicitly configured application workspaces."""
    return [
        {'name': Path(root).name or str(root), 'path': str(Path(root).resolve())}
        for root in settings.APP_RUNNER_ALLOWED_ROOTS
        if Path(root).is_dir()
    ]


def _resolve_allowed_path(raw: str) -> str:
    target = Path(raw).expanduser().resolve(strict=False)
    roots = [Path(root).expanduser().resolve(strict=False)
             for root in settings.APP_RUNNER_ALLOWED_ROOTS]
    if not roots:
        raise PermissionError('Server-side file browsing is disabled.')
    if not any(target == root or root in target.parents for root in roots):
        raise PermissionError('Path is outside the configured workspace roots.')
    return str(target)


@api_view(['GET'])
@permission_classes([IsAuthenticated])
def list_dir(request):
    """Browse the server filesystem to pick a folder.

    With no ``path``: return navigable roots (Windows drives / '/'). With a
    ``path``: return its immediate subdirectories plus the parent dir, so the
    client can drill up/down. Same localhost trust boundary as scan_folder —
    intentionally not sandboxed.
    """
    raw = (request.query_params.get('path') or '').strip()
    if not raw:
        return Response({'path': '', 'parent': '', 'roots': _list_roots(), 'dirs': []})

    try:
        path = _resolve_allowed_path(raw)
    except PermissionError as exc:
        return Response({'detail': str(exc)}, status=status.HTTP_403_FORBIDDEN)
    if not os.path.isdir(path):
        return Response({'detail': '不是有效目录'}, status=status.HTTP_400_BAD_REQUEST)
    try:
        entries = sorted(os.listdir(path))
    except PermissionError:
        return Response({'detail': '无权限访问该目录'}, status=status.HTTP_403_FORBIDDEN)

    dirs = [{'name': f, 'path': os.path.join(path, f)}
            for f in entries if os.path.isdir(os.path.join(path, f))]
    parent = os.path.dirname(path)
    # dirname of a Windows drive root ('C:\\') is itself → no further parent.
    if parent == path:
        parent = ''
    return Response({'path': path, 'parent': parent, 'roots': [], 'dirs': dirs})
