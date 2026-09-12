import pytest
from django.contrib.auth import get_user_model
from rest_framework.test import APIClient

from apps.applications.models import Application, ApplicationCategory
from apps.app_runner import executors as exec_mod
from apps.app_runner.models import Job

pytestmark = pytest.mark.django_db


@pytest.fixture
def app_and_user():
    user = get_user_model().objects.create_user(username='v', password='p')
    cat = ApplicationCategory.objects.create(name='Audio', slug='audio')
    Application.objects.create(
        category=cat, name='批量转录', slug='batch-transcribe',
        description='d', created_by=user)
    return user


def _client(user):
    c = APIClient()
    c.force_authenticate(user)
    return c


class _DoneExec:
    def run(self, job, sink):
        sink.emit('job.state', {'status': 'done'})


def test_create_job_validates_app_slug(app_and_user):
    c = _client(app_and_user)
    resp = c.post('/api/app-runner/jobs/', {'app_slug': 'no-such-app',
                                            'config': {}}, format='json')
    assert resp.status_code == 400


def test_create_job_starts_and_returns_id(app_and_user, monkeypatch, settings):
    from apps.app_runner import job_manager as jm_mod
    monkeypatch.setattr(jm_mod, '_broadcast', lambda *a, **k: None)
    settings.APP_RUNNER_INLINE_EXECUTION = False
    # Stub the executor via monkeypatch so the module-level EXECUTORS registry
    # is restored on teardown (avoids polluting other tests).
    monkeypatch.setitem(exec_mod.EXECUTORS, 'batch-transcribe', _DoneExec())

    c = _client(app_and_user)
    resp = c.post('/api/app-runner/jobs/',
                  {'app_slug': 'batch-transcribe',
                   'config': {'folder': '/tmp', 'model': 'tiny'}}, format='json')
    assert resp.status_code == 201
    assert 'id' in resp.data
    assert Job.objects.filter(app_slug='batch-transcribe').exists()

    c = _client(app_and_user)
    resp = c.post('/api/app-runner/jobs/',
                  {'app_slug': 'batch-transcribe',
                   'config': {'folder': '/tmp', 'model': 'tiny'}}, format='json')
    assert resp.status_code == 201
    assert 'id' in resp.data
    assert Job.objects.filter(app_slug='batch-transcribe').exists()


def test_get_job_status(app_and_user):
    user = app_and_user
    job = Job.objects.create(app_slug='batch-transcribe', config={}, owner=user)
    c = _client(user)
    resp = c.get(f'/api/app-runner/jobs/{job.id}/')
    assert resp.status_code == 200
    assert resp.data['id'] == str(job.id)


def test_stop_job(app_and_user, monkeypatch):
    from apps.app_runner import job_manager as jm_mod
    user = app_and_user
    job = Job.objects.create(app_slug='batch-transcribe', config={}, owner=user)
    seen = {}
    monkeypatch.setattr(jm_mod.job_manager, 'stop',
                        lambda jid: seen.__setitem__('x', jid) or True)
    c = _client(user)
    resp = c.post(f'/api/app-runner/jobs/{job.id}/stop/')
    assert resp.status_code == 204
    assert seen['x'] == str(job.id)


def test_scan_folder_lists_videos(app_and_user, tmp_path, settings):
    settings.APP_RUNNER_ALLOWED_ROOTS = [str(tmp_path)]
    (tmp_path / 'a.mp4').write_bytes(b'')
    (tmp_path / 'b.txt').write_text('x')
    c = _client(app_and_user)
    resp = c.post('/api/app-runner/fs/scan/', {'path': str(tmp_path)}, format='json')
    assert resp.status_code == 200
    names = [v['name'] for v in resp.data['videos']]
    assert names == ['a.mp4']
