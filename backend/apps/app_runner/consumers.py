from channels.generic.websocket import AsyncJsonWebsocketConsumer
from channels.db import database_sync_to_async

from .job_manager import job_manager
from .events import EVT_SNAPSHOT
from .models import Job, JobEvent


class JobConsumer(AsyncJsonWebsocketConsumer):
    """Streams job events to the browser; accepts {action:'stop'} from client."""

    async def connect(self):
        if self.scope.get('user') is None:
            await self.close(code=4401)
            return
        self.job_id = self.scope['url_route']['kwargs']['job_id']
        if not await self._owns_job():
            await self.close(code=4403)
            return
        self.group = f'job-{self.job_id}'
        await self.channel_layer.group_add(self.group, self.channel_name)
        await self.accept()
        await self._send_snapshot()

    async def disconnect(self, code):
        if getattr(self, 'group', None):
            await self.channel_layer.group_discard(self.group, self.channel_name)

    async def receive_json(self, content, **kwargs):
        if content.get('action') == 'stop':
            job_manager.stop(self.job_id)

    async def job_event(self, message):
        """Handler for group broadcasts sent by JobHandle._broadcast."""
        await self.send_json(message['event'])

    async def _send_snapshot(self):
        handle = job_manager.get_handle(self.job_id)
        if handle is not None:
            await self.send_json({'type': EVT_SNAPSHOT, **handle.snapshot()})
            return
        snapshot = await self._durable_snapshot()
        await self.send_json({'type': EVT_SNAPSHOT, **snapshot})

    @database_sync_to_async
    def _owns_job(self):
        return Job.objects.filter(id=self.job_id, owner=self.scope['user']).exists()

    @database_sync_to_async
    def _durable_snapshot(self):
        job = Job.objects.get(id=self.job_id, owner=self.scope['user'])
        progress = {'current': 0, 'total': 0, 'label': ''}
        items = {}
        logs = []
        for event in JobEvent.objects.filter(job=job).order_by('sequence')[:5000]:
            payload = event.payload
            if event.event_type == 'job.progress':
                progress = {key: payload.get(key, progress.get(key))
                            for key in ('current', 'total', 'label')}
            elif event.event_type == 'item.state':
                items[str(payload.get('id'))] = payload
            elif event.event_type == 'log':
                logs.append({'level': payload.get('level', 'info'),
                             'msg': payload.get('msg', '')})
        return {
            'status': job.status,
            'progress': progress,
            'items': list(items.values()),
            'logs': logs[-500:],
        }
