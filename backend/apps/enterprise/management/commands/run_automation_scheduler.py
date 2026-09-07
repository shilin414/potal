import time

from django.core.management.base import BaseCommand
from django.utils import timezone

from apps.enterprise.models import AutomationTrigger
from apps.enterprise.services import cron_matches, dispatch_automation


class Command(BaseCommand):
    help = 'Dispatch active scheduled automations.'

    def add_arguments(self, parser):
        parser.add_argument('--once', action='store_true')
        parser.add_argument('--poll-interval', type=float, default=30)

    def handle(self, *args, **options):
        while True:
            now = timezone.now().replace(second=0, microsecond=0)
            for trigger in AutomationTrigger.objects.filter(
                    is_active=True, trigger_type='schedule').select_related(
                        'organization__owner'):
                already_ran = trigger.last_triggered_at and \
                    trigger.last_triggered_at.replace(second=0, microsecond=0) >= now
                if not already_ran and cron_matches(trigger.schedule, now):
                    dispatch_automation(trigger, trigger.organization.owner, {})
            if options['once']:
                return
            time.sleep(options['poll_interval'])
