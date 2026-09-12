"""Database-backed job claiming for crash-safe external workers.

Claiming uses a compare-and-swap UPDATE (still queued -> running) rather
than SELECT ... FOR UPDATE SKIP LOCKED so it works identically on TiDB 8
and MySQL 8.
"""
from django.db import models
from django.utils import timezone

from .job_manager import job_manager
from .models import Job


def claim_next_job():
    candidate_ids = list(
        Job.objects.filter(
            status=Job.Status.PENDING,
            attempt__lt=models.F('max_attempts'),
        ).order_by('created_at').values_list('id', flat=True)[:10]
    )
    now = timezone.now()
    for job_id in candidate_ids:
        claimed = Job.objects.filter(
            pk=job_id, status=Job.Status.PENDING,
            attempt__lt=models.F('max_attempts'),
        ).update(status=Job.Status.RUNNING, heartbeat_at=now)
        if claimed:
            return Job.objects.get(pk=job_id)
    return None


def execute_next_job():
    job = claim_next_job()
    if job is None:
        return None
    return job_manager.start(job)
