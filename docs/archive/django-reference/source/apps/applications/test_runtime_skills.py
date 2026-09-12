"""Tests for filesystem-backed Codex and GraphFlow skill management."""
from pathlib import Path
from tempfile import TemporaryDirectory

from django.test import TestCase, override_settings
from rest_framework.test import APIClient

from apps.users.models import User


class RuntimeSkillApiTest(TestCase):
    def setUp(self):
        self.admin = User.objects.create_user(
            'skill-admin', password='secret', role=User.Role.ADMIN)
        self.viewer = User.objects.create_user(
            'skill-viewer', password='secret', role=User.Role.CREATOR)
        self.client = APIClient()
        self.client.force_authenticate(self.admin)

    @staticmethod
    def _write_skill(root: Path, slug: str, *, name: str = 'Test skill'):
        directory = root / slug
        directory.mkdir(parents=True)
        (directory / 'SKILL.md').write_text(
            f'---\nname: "{name}"\ndescription: "Description"\n---\n\n# Rules\n',
            encoding='utf-8',
        )
        return directory

    def test_lists_both_runtime_roots_and_reads_detail(self):
        with TemporaryDirectory() as codex_dir, TemporaryDirectory() as graphflow_dir:
            self._write_skill(Path(codex_dir), 'codex-skill', name='Codex skill')
            self._write_skill(
                Path(graphflow_dir), 'graphflow-skill', name='GraphFlow skill')
            with override_settings(
                CODEX_SKILLS_DIRECTORY=codex_dir,
                GRAPHFLOW_SKILLS_DIRECTORY=graphflow_dir,
            ):
                response = self.client.get('/api/apps/runtime-skills/')
                detail = self.client.get(
                    '/api/apps/runtime-skills/codex/codex-skill/')

        self.assertEqual(response.status_code, 200, response.data)
        self.assertTrue(response.data['canManage'])
        self.assertEqual(
            {item['provider'] for item in response.data['skills']},
            {'codex', 'graphflow'},
        )
        self.assertEqual(detail.status_code, 200, detail.data)
        self.assertEqual(detail.data['name'], 'Codex skill')
        self.assertIn('# Rules', detail.data['content'])
        self.assertEqual(detail.data['files'][0]['path'], 'SKILL.md')

    def test_admin_can_create_update_and_delete_skill(self):
        with TemporaryDirectory() as codex_dir, TemporaryDirectory() as graphflow_dir:
            with override_settings(
                CODEX_SKILLS_DIRECTORY=codex_dir,
                GRAPHFLOW_SKILLS_DIRECTORY=graphflow_dir,
            ):
                created = self.client.post('/api/apps/runtime-skills/', {
                    'provider': 'graphflow',
                    'slug': 'new-skill',
                    'name': 'New skill',
                    'description': 'Created by the API',
                }, format='json')
                updated = self.client.patch(
                    '/api/apps/runtime-skills/graphflow/new-skill/',
                    {'content': (
                        '---\nname: Updated\ndescription: Changed\n---\n\nNew rules\n'
                    )},
                    format='json',
                )
                deleted = self.client.delete(
                    '/api/apps/runtime-skills/graphflow/new-skill/')
                exists_after_delete = (
                    Path(graphflow_dir) / 'new-skill').exists()

        self.assertEqual(created.status_code, 201, created.data)
        self.assertEqual(created.data['name'], 'New skill')
        self.assertEqual(updated.status_code, 200, updated.data)
        self.assertEqual(updated.data['description'], 'Changed')
        self.assertEqual(deleted.status_code, 204)
        self.assertFalse(exists_after_delete)

    def test_non_admin_can_view_but_cannot_modify(self):
        self.client.force_authenticate(self.viewer)
        with TemporaryDirectory() as codex_dir, TemporaryDirectory() as graphflow_dir:
            self._write_skill(Path(codex_dir), 'visible-skill')
            with override_settings(
                CODEX_SKILLS_DIRECTORY=codex_dir,
                GRAPHFLOW_SKILLS_DIRECTORY=graphflow_dir,
            ):
                listed = self.client.get('/api/apps/runtime-skills/')
                created = self.client.post('/api/apps/runtime-skills/', {
                    'provider': 'codex',
                    'slug': 'forbidden-skill',
                    'name': 'Forbidden',
                }, format='json')
                deleted = self.client.delete(
                    '/api/apps/runtime-skills/codex/visible-skill/')

        self.assertEqual(listed.status_code, 200, listed.data)
        self.assertFalse(listed.data['canManage'])
        self.assertEqual(created.status_code, 403)
        self.assertEqual(deleted.status_code, 403)

    def test_rejects_path_traversal_slug(self):
        with TemporaryDirectory() as codex_dir, TemporaryDirectory() as graphflow_dir:
            with override_settings(
                CODEX_SKILLS_DIRECTORY=codex_dir,
                GRAPHFLOW_SKILLS_DIRECTORY=graphflow_dir,
            ):
                response = self.client.post('/api/apps/runtime-skills/', {
                    'provider': 'codex',
                    'slug': '../outside',
                    'name': 'Outside',
                }, format='json')

        self.assertEqual(response.status_code, 400)
        self.assertIn('slug', response.data)
