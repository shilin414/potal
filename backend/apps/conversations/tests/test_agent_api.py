"""Authorization and control tests for the Codex-shaped Agent API v2."""
from types import SimpleNamespace
from unittest.mock import MagicMock, patch

from django.contrib.auth import get_user_model
from django.test import TestCase
from rest_framework.test import APIClient

from apps.conversations.models import (
    AgentItem,
    AgentServerRequest,
    AgentThread,
    AgentTurn,
    Conversation,
)

User = get_user_model()


class AgentProtocolApiTest(TestCase):
    def setUp(self):
        self.user = User.objects.create_user(
            username='protocol-owner', password='testpass123')
        self.other_user = User.objects.create_user(
            username='protocol-other', password='testpass123')
        self.client = APIClient()
        self.client.force_authenticate(user=self.user)
        self.conversation = Conversation.objects.create(
            user=self.user, title='Protocol conversation')
        self.thread = AgentThread.objects.create(
            conversation=self.conversation,
            provider='graphflow',
            status=AgentThread.Status.IDLE,
        )
        self.turn = AgentTurn.objects.create(
            thread=self.thread,
            status=AgentTurn.Status.COMPLETED,
            input=[{'type': 'text', 'text': 'Hello'}],
        )
        self.item = AgentItem.objects.create(
            turn=self.turn,
            remote_id='message-1',
            item_type='agentMessage',
            ordinal=0,
            content='Hi',
            payload={'id': 'message-1', 'type': 'agentMessage', 'text': 'Hi'},
        )
        self.server_request = AgentServerRequest.objects.create(
            thread=self.thread,
            turn=self.turn,
            remote_id='request-1',
            method='tool/requestUserInput',
            params={
                'requestId': 'request-1',
                'question': {'question': 'Continue?', 'options': []},
            },
        )

    def test_owner_can_list_threads_and_turns(self):
        threads = self.client.get('/api/agent/v2/threads/')
        turns = self.client.get(
            f'/api/agent/v2/threads/{self.thread.id}/turns/')
        standalone_turn = self.client.get(
            f'/api/agent/v2/turns/{self.turn.id}/')

        self.assertEqual(threads.status_code, 200)
        thread_rows = threads.data.get('results', threads.data)
        self.assertEqual(thread_rows[0]['conversationId'], self.conversation.id)
        self.assertEqual(turns.status_code, 200)
        self.assertEqual(turns.data[0]['items'][0]['id'], 'message-1')
        self.assertEqual(standalone_turn.status_code, 200)
        self.assertEqual(standalone_turn.data['threadId'], str(self.thread.id))

    def test_in_progress_status_uses_protocol_casing(self):
        active_turn = AgentTurn.objects.create(
            thread=self.thread, status=AgentTurn.Status.IN_PROGRESS)

        response = self.client.get(f'/api/agent/v2/turns/{active_turn.id}/')

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data['status'], 'inProgress')

    def test_non_owner_receives_not_found(self):
        self.client.force_authenticate(user=self.other_user)

        self.assertEqual(
            self.client.get(f'/api/agent/v2/threads/{self.thread.id}/').status_code,
            404,
        )
        self.assertEqual(
            self.client.get(f'/api/agent/v2/turns/{self.turn.id}/').status_code,
            404,
        )
        self.assertEqual(
            self.client.get(
                f'/api/agent/v2/server-requests/{self.server_request.id}/'
            ).status_code,
            404,
        )

    @patch('apps.conversations.agent_views.session_registry.get')
    def test_resolve_request_resumes_session_once(self, get_session):
        session = MagicMock()
        session.resume.return_value = SimpleNamespace(value='accepted')
        get_session.return_value = session

        url = (
            f'/api/agent/v2/server-requests/{self.server_request.id}/resolve/'
        )
        response = self.client.post(
            url, {'selections': ['continue']}, format='json')

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data['request']['status'], 'resolved')
        session.resume.assert_called_once_with(
            text='', selections=['continue'])
        self.server_request.refresh_from_db()
        self.assertEqual(
            self.server_request.status, AgentServerRequest.Status.RESOLVED)
        self.assertEqual(self.client.post(
            url, {'selections': ['continue']}, format='json').status_code, 409)

    @patch('apps.conversations.agent_views.session_registry.get', return_value=None)
    def test_resolve_request_requires_active_session(self, _get_session):
        response = self.client.post(
            f'/api/agent/v2/server-requests/{self.server_request.id}/resolve/',
            {'text': 'continue'},
            format='json',
        )

        self.assertEqual(response.status_code, 409)
        self.server_request.refresh_from_db()
        self.assertEqual(
            self.server_request.status, AgentServerRequest.Status.PENDING)
