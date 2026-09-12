"""Executor registry: maps app_slug -> executor with run(job, sink)."""
import os
from typing import Protocol

from .events import EventSink

VIDEO_EXTENSIONS = {'.mp4', '.avi', '.mkv', '.mov', '.flv', '.wmv', '.webm', '.ts', '.m4v'}

# Loaded only when the optional batch-transcription executor is invoked. The
# module attribute is kept injectable for tests and optional integrations.
BatchTranscribeCore = None


def _batch_transcribe_core():
    global BatchTranscribeCore
    if BatchTranscribeCore is None:
        try:
            from creation_core.transcribe import BatchTranscribeCore as core
        except ImportError as exc:
            raise RuntimeError(
                '批量转录功能不可用：未安装可选依赖 creation_core。'
            ) from exc
        BatchTranscribeCore = core
    return BatchTranscribeCore


class AppExecutor(Protocol):
    def run(self, job, sink: EventSink) -> None: ...


class BatchTranscribeExecutor:
    """Scan the configured server-side folder, then run BatchTranscribeCore."""

    def run(self, job, sink: EventSink) -> None:
        folder = job.config.get('folder', '')
        model = job.config.get('model', 'base')
        language = job.config.get('language') or None  # '' -> None = auto-detect
        output_dir = job.config.get('output_dir') or os.path.join(folder, 'transcripts')

        videos = sorted(
            os.path.join(folder, f)
            for f in os.listdir(folder)
            if os.path.splitext(f)[1].lower() in VIDEO_EXTENSIONS
        ) if os.path.isdir(folder) else []

        core_class = _batch_transcribe_core()
        core = core_class(videos, output_dir, model_size=model, language=language)
        core.run(sink)


EXECUTORS: dict[str, AppExecutor] = {
    'batch-transcribe': BatchTranscribeExecutor(),
}
