import threading

from apps.app_runner import job_manager as jm_mod
from apps.app_runner.job_manager import JobHandle


class _CallableExec:
    """Test executor wrapping a function as run(job, sink)."""
    def __init__(self, fn):
        self._fn = fn
    def run(self, job, sink):
        self._fn(job, sink)


def test_handle_records_and_snapshots(monkeypatch):
    monkeypatch.setattr(jm_mod, '_broadcast', lambda job_id, event: None)
    h = JobHandle(job_id='x')
    h.record({'type': 'job.state', 'status': 'running'})
    h.record({'type': 'job.progress', 'current': 1, 'total': 3, 'label': 'a'})
    h.record({'type': 'item.state', 'id': 'v1', 'name': 'a', 'status': 'done',
              'result': '/o/a.txt', 'error': ''})
    h.record({'type': 'log', 'level': 'info', 'msg': 'hi'})

    snap = h.snapshot()
    assert snap['status'] == 'running'
    assert snap['progress']['current'] == 1
    # items is a LIST per the state.snapshot protocol
    assert isinstance(snap['items'], list)
    assert snap['items'][0]['id'] == 'v1'
    assert snap['items'][0]['status'] == 'done'
    assert snap['logs'][-1]['msg'] == 'hi'


def test_handle_cancel_flag():
    h = JobHandle(job_id='x')
    assert h.is_cancelled is False
    h.cancel()
    assert h.is_cancelled is True


def test_manager_runs_executor_and_marks_done(monkeypatch):
    monkeypatch.setattr(jm_mod, '_broadcast', lambda job_id, event: None)

    def fake_exec(job, sink):
        sink.emit('job.state', {'status': 'running'})
        sink.emit('log', {'level': 'info', 'msg': 'worked'})

    class FakeJob:
        id = '11111111-1111-1111-1111-111111111111'
        app_slug = 'batch-transcribe'

    monkeypatch.setitem(jm_mod.EXECUTORS, 'batch-transcribe', _CallableExec(fake_exec))

    mgr = jm_mod.JobManager()
    mgr._reset()
    mgr.start(FakeJob())
    mgr._join_all(timeout=2.0)

    h = mgr.get_handle('11111111-1111-1111-1111-111111111111')
    assert h.snapshot()['status'] == 'done'
    assert any(e['msg'] == 'worked' for e in h.snapshot()['logs'])


def test_manager_stop_sets_cancelled(monkeypatch):
    monkeypatch.setattr(jm_mod, '_broadcast', lambda job_id, event: None)

    started = threading.Event()

    def slow_exec(job, sink):
        started.set()
        import time
        for _ in range(50):
            if sink.cancelled:
                sink.emit('job.state', {'status': 'stopped'})
                return
            time.sleep(0.01)

    class FakeJob:
        id = '22222222-2222-2222-2222-222222222222'
        app_slug = 'batch-transcribe'

    monkeypatch.setitem(jm_mod.EXECUTORS, 'batch-transcribe', _CallableExec(slow_exec))
    mgr = jm_mod.JobManager()
    mgr._reset()
    mgr.start(FakeJob())
    started.wait(timeout=2.0)
    assert mgr.stop('22222222-2222-2222-2222-222222222222') is True
    mgr._join_all(timeout=2.0)
    h = mgr.get_handle('22222222-2222-2222-2222-222222222222')
    assert h.snapshot()['status'] == 'stopped'
