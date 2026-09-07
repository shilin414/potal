"""Database-backed job claiming for crash-safe external workers."""
from django.db import models, transaction
from django.utils import timezone

from .job_manager import job_manager
from .models import Job


@transaction.atomic
def claim_next_job():
    job = Job.objects.select_for_update(skip_locked=True).filter(
        status=Job.Status.PENDING,
        attempt__lt=models.F('max_attempts'),
    ).order_by('created_at').first()
    if job is None:
        return None
    job.status = Job.Status.RUNNING
    job.heartbeat_at = timezone.now()
    job.save(update_fields=['status', 'heartbeat_at'])
    return job


def execute_next_job():
    job = claim_next_job()
    if job is None:
        return None
    return job_manager.start(job)
