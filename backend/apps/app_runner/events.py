"""Event protocol used by the application runner.

Keep this small protocol local so the web service can start without the
optional ``creation_core`` package. Optional executors may still consume the
same event names when their runtime dependency is installed.
"""
from typing import Protocol, runtime_checkable

EVT_JOB_STATE = 'job.state'
EVT_JOB_PROGRESS = 'job.progress'
EVT_ITEM_STATE = 'item.state'
EVT_LOG = 'log'
EVT_SNAPSHOT = 'state.snapshot'


@runtime_checkable
class EventSink(Protocol):
    def emit(self, type: str, payload: dict) -> None: ...

    @property
    def cancelled(self) -> bool: ...
