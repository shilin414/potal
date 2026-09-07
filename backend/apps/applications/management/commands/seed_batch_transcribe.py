"""Idempotently seed the 批量转录 (batch-transcribe) Application + its category."""
from django.contrib.auth import get_user_model
from django.core.management.base import BaseCommand

from apps.applications.models import Application, ApplicationCategory

SLUG = 'batch-transcribe'


class Command(BaseCommand):
    help = f'Seed the {SLUG} application row (idempotent).'

    def handle(self, *args, **options):
        User = get_user_model()
        owner = User.objects.first()
        if owner is None:
            self.stderr.write('No user exists yet — register a user, then re-run.')
            return

        cat, _ = ApplicationCategory.objects.get_or_create(
            slug='audio',
            defaults={'name': '音频工具', 'order': 30},
        )
        app, created = Application.objects.update_or_create(
            slug=SLUG,
            defaults={
                'category': cat,
                'name': '批量转录',
                'description': '基于 Whisper 的批量视频转文字/字幕（.txt/.srt）。',
                'icon': '🎙️',
                'color': '#2e7d32',
                'tags': ['转录', 'Whisper', '字幕'],
                'developer': 'creation_master',
                'is_public': True,
                'kind': Application.Kind.TASK,
                'renderer_key': 'batch-transcribe',
                'executor_key': 'batch-transcribe',
                'created_by': owner,
            },
        )
        self.stdout.write(self.style.SUCCESS(
            f"{'Created' if created else 'Updated'} application {SLUG} (id={app.id})."))
