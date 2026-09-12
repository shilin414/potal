"""
Tests for projects app.
"""
from django.test import TestCase
from django.contrib.auth import get_user_model
from apps.projects.models import Project

User = get_user_model()


class ProjectModelTest(TestCase):
    """
    Test cases for Project model.
    """

    def setUp(self):
        """
        Set up test data.
        """
        self.user = User.objects.create_user(
            username='testuser',
            email='test@example.com',
            password='testpass123'
        )
        self.project = Project.objects.create(
            user=self.user,
            title='Test Project',
            description='A test project',
            status='draft'
        )

    def test_project_creation(self):
        """
        Test that a project can be created.
        """
        self.assertEqual(self.project.user, self.user)
        self.assertEqual(self.project.title, 'Test Project')
        self.assertEqual(self.project.status, 'draft')

    def test_project_str(self):
        """
        Test the __str__ method returns name.
        """
        self.assertEqual(str(self.project), 'Test Project')

    def test_project_timestamps(self):
        """
        Test that created_at and updated_at are set.
        """
        self.assertIsNotNone(self.project.created_at)
        self.assertIsNotNone(self.project.updated_at)
