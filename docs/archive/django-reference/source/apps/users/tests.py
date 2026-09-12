"""
Tests for users app.
"""
from django.test import TestCase
from django.contrib.auth import get_user_model

User = get_user_model()


class UserModelTest(TestCase):
    """
    Test cases for User model.
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

    def test_user_creation(self):
        """
        Test that a user can be created.
        """
        self.assertTrue(self.user.check_password('testpass123'))
        self.assertEqual(self.user.username, 'testuser')
        self.assertEqual(self.user.email, 'test@example.com')

    def test_user_str(self):
        """
        Test the __str__ method returns username.
        """
        self.assertTrue(str(self.user).startswith('testuser'))
