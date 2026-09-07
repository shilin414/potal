from datetime import timedelta

from django.core.management.base import BaseCommand
from django.utils import timezone

from apps.conversations.models import Conversation
from apps.enterprise.models import AuditLog, GovernancePolicy, RunTrace


class Command(BaseCommand):
    help = 'Delete tenant data beyond configured retention windows.'

    def add_arguments(self, parser):
        parser.add_argument('--dry-run', action='store_true')

    def handle(self, *args, **options):
        total = 0
        for policy in GovernancePolicy.objects.select_related('organization'):
            cutoff = timezone.now() - timedelta(days=policy.retention_days)
            querysets = [
                Conversation.objects.filter(organization=policy.organization,
                                            updated_at__lt=cutoff),
                RunTrace.objects.filter(organization=policy.organization,
                                        created_at__lt=cutoff),
                AuditLog.objects.filter(organization=policy.organization,
                                        created_at__lt=cutoff),
            ]
            for queryset in querysets:
                count = queryset.count()
                total += count
                if not options['dry_run']:
                    queryset.delete()
        self.stdout.write(f'{"Would delete" if options["dry_run"] else "Deleted"} {total} records')
