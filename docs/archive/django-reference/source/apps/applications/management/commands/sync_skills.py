import hashlib
import re
from pathlib import Path

from django.conf import settings
from django.core.management.base import BaseCommand, CommandError
from apps.applications.models import Skill
from apps.enterprise.models import Membership
from apps.users.models import User


class Command(BaseCommand):
    help = '将 GRAPHFLOW_SKILLS_DIRECTORY 中的 SKILL.md 同步到 Skill 当前内容。'

    def add_arguments(self, parser):
        parser.add_argument('--owner', help='Skill 所有者用户名')
        parser.add_argument('--directory', help='覆盖 Skill 根目录')

    def handle(self, *args, **options):
        root = Path(options.get('directory') or settings.GRAPHFLOW_SKILLS_DIRECTORY)
        if not root.is_dir():
            raise CommandError(f'Skill 目录不存在：{root}')
        owner = (User.objects.filter(username=options.get('owner')).first()
                 if options.get('owner') else User.objects.filter(is_superuser=True).first())
        owner = owner or User.objects.order_by('id').first()
        if owner is None:
            raise CommandError('请先创建用户，或通过 --owner 指定所有者。')
        membership = Membership.objects.filter(
            user=owner, is_active=True).select_related('organization').first()
        count = 0
        for skill_file in sorted(root.glob('*/SKILL.md')):
            raw = skill_file.read_text(encoding='utf-8')
            digest = hashlib.sha256(raw.encode('utf-8')).hexdigest()
            slug = skill_file.parent.name
            name = self._frontmatter(raw, 'name') or slug
            description = self._frontmatter(raw, 'description')
            skill, _ = Skill.objects.update_or_create(
                organization=membership.organization if membership else None,
                slug=slug,
                defaults={
                    'name': name,
                    'description': description,
                    'visibility': (Skill.Visibility.ORGANIZATION
                                   if membership else Skill.Visibility.PUBLIC),
                    'owner': owner,
                    'is_active': True,
                    'source_type': Skill.SourceType.BUNDLED,
                    'source_uri': str(skill_file.resolve()),
                    'artifact_key': str(skill_file.parent.resolve()),
                    'manifest': {'entrypoint': 'SKILL.md'},
                    'content_hash': digest,
                })
            count += 1
        self.stdout.write(self.style.SUCCESS(f'已同步 {count} 个 Skill。'))

    @staticmethod
    def _frontmatter(content, key):
        match = re.search(rf'^\s*{re.escape(key)}:\s*["\']?(.+?)["\']?\s*$',
                          content, re.MULTILINE)
        return match.group(1).strip() if match else ''
