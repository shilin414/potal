from django.contrib.auth import get_user_model
from django.test import TestCase

from apps.marketplace.models import TemplateReview, UserFavorite
from apps.templates.models import Template, TemplateCategory


class CaseMarketplaceModelTest(TestCase):
    def setUp(self):
        self.user = get_user_model().objects.create_user(
            username='reader', password='testpass')
        owner = get_user_model().objects.create_user(
            username='case-owner', password='testpass')
        category = TemplateCategory.objects.create(name='文章', slug='articles')
        self.case = Template.objects.create(
            category=category,
            title='Test Case',
            summary='A case for the library',
            created_by=owner,
            status='published',
        )

    def test_case_can_be_reviewed(self):
        review = TemplateReview.objects.create(
            template=self.case, user=self.user, rating=5, comment='拆解清晰')
        self.assertIn('Test Case', str(review))
        self.assertEqual(review.rating, 5)

    def test_case_can_be_favorited(self):
        favorite = UserFavorite.objects.create(template=self.case, user=self.user)
        self.assertIn('Test Case', str(favorite))
