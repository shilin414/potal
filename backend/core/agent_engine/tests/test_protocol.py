"""Contract tests for the Codex-shaped internal agent protocol."""
from unittest import TestCase

from core.agent_engine.adapters.base import (
    AgentEvent,
    EventType,
    ProgressCategory,
)
from core.agent_engine.protocol import ProtocolMethod, ProtocolProjector


class ProtocolProjectorTest(TestCase):
    def setUp(self):
        self.projector = ProtocolProjector(thread_id="thread-1", turn_id="turn-1")

    def test_projects_message_delta_and_turn_completion(self):
        started = self.projector.start_turn()
        delta_events = self.projector.project(AgentEvent(
            type=EventType.PROGRESS,
            agent_id="thread-1",
            request_id="message-1",
            content="Hello",
            progress_category=ProgressCategory.CONTENT,
        ))
        completed_events = self.projector.project(AgentEvent(
            type=EventType.COMPLETED,
            agent_id="thread-1",
            usage={"total_tokens": 3},
        ))

        self.assertEqual(started.method, ProtocolMethod.TURN_STARTED)
        self.assertEqual(
            [event.method for event in delta_events],
            [ProtocolMethod.ITEM_STARTED, ProtocolMethod.AGENT_MESSAGE_DELTA],
        )
        self.assertEqual(delta_events[-1].params, {
            "threadId": "thread-1",
            "turnId": "turn-1",
            "itemId": "message-1",
            "delta": "Hello",
        })
        self.assertEqual(
            [event.method for event in completed_events],
            [ProtocolMethod.ITEM_COMPLETED, ProtocolMethod.TURN_COMPLETED],
        )
        self.assertEqual(
            completed_events[-1].params["turn"]["status"], "completed"
        )

    def test_tool_creates_typed_item(self):
        started = self.projector.project(AgentEvent(
            type=EventType.TOOL_START,
            agent_id="thread-1",
            tool_call={
                "id": "tool-1",
                "name": "command",
                "input": '{"command":"pwd","cwd":"D:/workspace"}',
                "status": "running",
            },
        ))
        completed = self.projector.project(AgentEvent(
            type=EventType.TOOL_END,
            agent_id="thread-1",
            tool_call={
                "id": "tool-1",
                "name": "command",
                "input": '{"command":"pwd","cwd":"D:/workspace"}',
                "result": "D:/workspace",
                "status": "completed",
            },
        ))

        self.assertEqual(started[0].params["item"]["type"], "commandExecution")
        self.assertEqual(
            completed[0].params["item"]["aggregatedOutput"], "D:/workspace"
        )

    def test_question_preserves_request_correlation(self):
        events = self.projector.project(AgentEvent(
            type=EventType.QUESTION,
            agent_id="thread-1",
            request_id="request-1",
            question={"question": "Continue?", "options": []},
        ))

        self.assertEqual(events[0].method, ProtocolMethod.REQUEST_USER_INPUT)
        self.assertEqual(events[0].params["requestId"], "request-1")

    def test_snapshot_content_is_converted_to_true_deltas(self):
        first = self.projector.project(AgentEvent(
            type=EventType.PROGRESS,
            agent_id="thread-1",
            content="Hello",
            progress_category=ProgressCategory.CONTENT,
            extras={"content_mode": "snapshot"},
        ))
        second = self.projector.project(AgentEvent(
            type=EventType.PROGRESS,
            agent_id="thread-1",
            content="Hello, world",
            progress_category=ProgressCategory.CONTENT,
            extras={"content_mode": "snapshot"},
        ))

        self.assertEqual(first[-1].params["delta"], "Hello")
        self.assertEqual(second[-1].params["delta"], ", world")
        self.assertEqual(
            self.projector.snapshot_items()[0]["text"], "Hello, world"
        )

    def test_sse_uses_sequence_as_replay_cursor(self):
        event = self.projector.start_turn()

        self.assertIn("id: 1\n", event.to_sse())
        self.assertIn("event: turn/started\n", event.to_sse())
