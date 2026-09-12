from apps.app_runner import executors as exec_mod
from apps.app_runner.executors import EXECUTORS, BatchTranscribeExecutor


class _Sink:
    def __init__(self):
        self.events = []
    def emit(self, type, payload):
        self.events.append({'type': type, **payload})
    @property
    def cancelled(self):
        return False


class _Job:
    def __init__(self, config):
        self.id = '33333333-3333-3333-3333-333333333333'
        self.app_slug = 'batch-transcribe'
        self.config = config


def test_executor_scans_folder_and_runs_core(monkeypatch, tmp_path):
    (tmp_path / 'a.mp4').write_bytes(b'')
    (tmp_path / 'b.txt').write_text('ignore me')  # non-video

    seen = {}

    class FakeCore:
        def __init__(self, video_files, output_dir, model_size='base', language=None):
            seen['video_files'] = list(video_files)
            seen['output_dir'] = output_dir
            seen['model_size'] = model_size
            seen['language'] = language
        def run(self, sink):
            sink.emit('job.state', {'status': 'done'})

    monkeypatch.setattr(exec_mod, 'BatchTranscribeCore', FakeCore)

    job = _Job({'folder': str(tmp_path), 'model': 'tiny', 'language': 'zh'})
    BatchTranscribeExecutor().run(job, _Sink())

    assert len(seen['video_files']) == 1
    assert seen['video_files'][0].endswith('a.mp4')
    assert seen['model_size'] == 'tiny'
    assert seen['language'] == 'zh'


def test_executors_registry_has_batch_transcribe():
    assert 'batch-transcribe' in EXECUTORS
    assert isinstance(EXECUTORS['batch-transcribe'], BatchTranscribeExecutor)
