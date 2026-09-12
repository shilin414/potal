"""
Aily execution worker — asyncio plane (no OS thread per stream).

Claims queued runs via CAS, executes them through AilyDispatcher, and
maintains lease heartbeats. Runs until stopped.
"""
import asyncio
import logging
import signal
import uuid

import asgiref.sync
from django.conf import settings
from django.core.management.base import BaseCommand

logger = logging.getLogger(__name__)

PROVIDER = 'feishu_aily'


class Command(BaseCommand):
    help = 'Run the Aily execution worker (asyncio plane)'

    def add_arguments(self, parser):
        parser.add_argument('--concurrency', type=int, default=20,
                            help='max concurrent run executions')
        parser.add_argument('--poll-interval', type=float, default=0.5,
                            help='seconds between queue scans')

    def handle(self, *args, **options):
        self.concurrency = options['concurrency']
        self.poll_interval = options['poll_interval']
        self.worker_id = f'aily-{uuid.uuid4().hex[:12]}'
        self.stopping = asyncio.Event()

        asyncio.run(self.main())

    async def main(self):
        logger.info('aily worker %s starting (concurrency=%s)',
                    self.worker_id, self.concurrency)

        loop = asyncio.get_running_loop()
        for sig in (signal.SIGINT, signal.SIGTERM):
            try:
                loop.add_signal_handler(sig, self.stopping.set)
            except NotImplementedError:  # Windows
                pass

        semaphore = asyncio.Semaphore(self.concurrency)
        in_flight: set[asyncio.Task] = set()
        next_recovery_at = 0.0

        while not self.stopping.is_set():
            if loop.time() >= next_recovery_at:
                recovered = await asgiref.sync.sync_to_async(
                    self._recover_expired_sync)()
                if recovered:
                    logger.warning('recovered %d expired run leases', recovered)
                next_recovery_at = loop.time() + max(
                    settings.RUN_LEASE_HEARTBEAT_SECONDS, 10)
            claimed_any = False
            while len(in_flight) < self.concurrency:
                run = await self._claim_next()
                if run is None:
                    break
                claimed_any = True
                task = asyncio.create_task(self._execute_guarded(semaphore, run))
                in_flight.add(task)
                task.add_done_callback(in_flight.discard)
            await asyncio.sleep(
                self.poll_interval if claimed_any else self.poll_interval * 2)

        logger.info('aily worker %s draining %d tasks', self.worker_id,
                    len(in_flight))
        if in_flight:
            await asyncio.gather(*in_flight, return_exceptions=True)
        logger.info('aily worker %s stopped', self.worker_id)

    async def _claim_next(self):
        return await asgiref.sync.sync_to_async(self._claim_next_sync)()

    def _claim_next_sync(self):
        from apps.execution.services import RunManager
        try:
            return RunManager.claim_next(
                self.worker_id, PROVIDER,
                lease_seconds=settings.RUN_LEASE_SECONDS)
        except Exception:  # noqa: BLE001
            logger.exception('claim failed')
            return None

    async def _execute_guarded(self, semaphore, run):
        async with semaphore:
            heartbeat = asyncio.create_task(self._heartbeat_loop(run))
            try:
                await self._execute(run)
            except Exception:  # noqa: BLE001
                logger.exception('run %s execution crashed', run.pk)
                await asgiref.sync.sync_to_async(self._fail_run)(run)
            finally:
                heartbeat.cancel()
                try:
                    await heartbeat
                except asyncio.CancelledError:
                    pass

    async def _execute(self, run):
        from django_redis import get_redis_connection
        from integrations.aily.dispatcher import AilyDispatcher

        redis = await asgiref.sync.sync_to_async(get_redis_connection)(
            'default')
        dispatcher = AilyDispatcher(redis)
        await dispatcher.execute(run)

    async def _heartbeat_loop(self, run):
        try:
            while True:
                await asyncio.sleep(settings.RUN_LEASE_HEARTBEAT_SECONDS)
                ok = await asgiref.sync.sync_to_async(self._heartbeat_sync)(run)
                if not ok:
                    logger.warning('heartbeat lost for run %s', run.pk)
                    return
        except asyncio.CancelledError:
            raise

    def _heartbeat_sync(self, run) -> bool:
        from apps.execution.services import RunManager
        return RunManager.heartbeat(
            run, self.worker_id, lease_seconds=settings.RUN_LEASE_SECONDS)

    def _recover_expired_sync(self) -> int:
        from apps.execution.services import RunManager
        return RunManager.recover_expired_leases()

    def _fail_run(self, run):
        from apps.execution.models import Run
        from apps.execution.services import RunManager
        RunManager.finish(
            run, Run.Status.FAILED,
            error_code='worker_crash',
            error_message='worker crashed while executing run')
