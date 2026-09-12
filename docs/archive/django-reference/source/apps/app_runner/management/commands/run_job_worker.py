import time

from django.core.management.base import BaseCommand

from apps.app_runner.job_manager import job_manager
from apps.app_runner.worker import execute_next_job


class Command(BaseCommand):
    help = 'Run the durable application job worker.'

    def add_arguments(self, parser):
        parser.add_argument('--poll-interval', type=float, default=1.0)
        parser.add_argument('--once', action='store_true')

    def handle(self, *args, **options):
        while True:
            handle = execute_next_job()
            if handle is not None:
                thread = job_manager._threads.get(handle.job_id)
                if thread:
                    thread.join()
            if options['once']:
                break
            if handle is None:
                time.sleep(options['poll_interval'])
