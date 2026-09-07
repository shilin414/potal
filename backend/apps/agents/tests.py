"""
Tests for agents app.
"""
from django.test import TestCase
from apps.agents.models import Agent


class AgentModelTest(TestCase):
    """
    Test cases for Agent model.
    """

    def setUp(self):
        """
        Set up test data.
        """
        self.agent = Agent.objects.create(
            name='Test Agent',
            description='A test agent',
            agent_type='test',
            config={'key': 'value'}
        )

    def test_agent_creation(self):
        """
        Test that an agent can be created.
        """
        self.assertEqual(self.agent.name, 'Test Agent')
        self.assertEqual(self.agent.agent_type, 'test')
        self.assertEqual(self.agent.config, {'key': 'value'})

    def test_agent_str(self):
        """
        Test the __str__ method returns name.
        """
        self.assertEqual(str(self.agent), 'Test Agent')

    def test_agent_timestamps(self):
        """
        Test that created_at and updated_at are set.
        """
        self.assertIsNotNone(self.agent.created_at)
        self.assertIsNotNone(self.agent.updated_at)
