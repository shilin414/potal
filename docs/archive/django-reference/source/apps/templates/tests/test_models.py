from django.contrib.auth import get_user_model
from django.test import TestCase

from apps.templates.models import Template, TemplateAnalysisSection, TemplateCategory


class TemplateModelTest(TestCase):
    def setUp(self):
        user = get_user_model().objects.create_user(
            username='case-owner', password='testpass')
        category = TemplateCategory.objects.create(
            name='文章', slug='articles', description='文章案例')
        self.template = Template.objects.create(
            title='Test Case',
            summary='A real content case',
            category=category,
            created_by=user,
            status='published',
            content_type='article',
            reusable_patterns=['具体场景开头'],
        )
        TemplateAnalysisSection.objects.create(
            template=self.template,
            section_type='hook',
            title='开头钩子',
            content='从具体场景切入。',
        )

    def test_case_and_analysis_creation(self):
        self.assertEqual(str(self.template), 'Test Case')
        self.assertEqual(self.template.reusable_patterns, ['具体场景开头'])
        self.assertEqual(self.template.analysis_sections.count(), 1)

    def test_timestamps_are_set(self):
        self.assertIsNotNone(self.template.created_at)
        self.assertIsNotNone(self.template.updated_at)
