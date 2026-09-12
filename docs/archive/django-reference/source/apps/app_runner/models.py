"""Models for the app_runner (job execution over websockets)."""
import uuid

from django.conf import settings
from django.db import models


class Job(models.Model):
    class Status(models.TextChoices):
        PENDING = 'pending', '等待'
        RUNNING = 'running', '运行中'
        DONE = 'done', '完成'
        ERROR = 'error', '错误'
        STOPPED = 'stopped', '已停止'

    id = models.UUIDField(primary_key=True, default=uuid.uuid4, editable=False)
    app_slug = models.SlugField(max_length=100, db_index=True)
    status = models.CharField(max_length=20, choices=Status.choices, default=Status.PENDING)
    config = models.JSONField(default=dict, blank=True)
    error = models.TextField(blank=True, default='')
    owner = models.ForeignKey(
        settings.AUTH_USER_MODEL, on_delete=models.SET_NULL, null=True, blank=True,
        related_name='runner_jobs')
    organization = models.ForeignKey(
        'enterprise.Organization', on_delete=models.CASCADE, null=True, blank=True,
        related_name='jobs'
    )
    attempt = models.PositiveIntegerField(default=0)
    max_attempts = models.PositiveIntegerField(default=3)
    heartbeat_at = models.DateTimeField(null=True, blank=True)
    created_at = models.DateTimeField(auto_now_add=True)
    finished_at = models.DateTimeField(null=True, blank=True)

    class Meta:
        db_table = 'app_runner_jobs'
        ordering = ['-created_at']

    def __str__(self):
        return f'{self.app_slug}<{self.status}>'


class JobEvent(models.Model):
    """Durable ordered execution event for reconnect and auditing."""
    id = models.BigAutoField(primary_key=True)
    job = models.ForeignKey(Job, on_delete=models.CASCADE, related_name='events')
    sequence = models.PositiveBigIntegerField()
    event_type = models.CharField(max_length=80, db_index=True)
    payload = models.JSONField(default=dict)
    created_at = models.DateTimeField(auto_now_add=True, db_index=True)

    class Meta:
        db_table = 'app_runner_job_events'
        ordering = ['sequence']
        constraints = [models.UniqueConstraint(
            fields=['job', 'sequence'], name='unique_job_event_sequence')]
