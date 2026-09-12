"""Bootstrap the built-in Aily provider and its runtime bindings.

Two ways to wire an Application to an Aily custom agent:

    # 1) 从命令行（CI / 首次部署）
    python manage.py bootstrap_runtime_catalog \
        --application-slug sales-assistant --agent-id agent_xxxxxxxx

    # 2) 从 Django Admin（日常运维，推荐）
    /django-admin/ → 应用 → 选应用 → 「运行时绑定」内联 → external_resource_id
                   → 应用 → 「工作台默认主智能体」勾选 is_default_agent

`--list` 打印当前所有应用与其绑定的智能体，用于排查"到底在跑哪个 agent"。
"""
from django.conf import settings
from django.core.exceptions import ValidationError
from django.core.management.base import BaseCommand, CommandError

from apps.applications.models import Application
from apps.catalog.models import (
    ApplicationRuntimeBinding,
    ArtifactPolicy,
    ExecutionMode,
    IdentityMode,
    Provider,
    RuntimeType,
    SessionPolicy,
)
from apps.catalog.providers import AILY_AGENT_CAPABILITIES


class Command(BaseCommand):
    help = 'Create/update built-in providers and bind applications to Aily agents'

    def add_arguments(self, parser):
        parser.add_argument(
            '--application-slug', default='creative-chat',
            help='Application that should use Aily')
        parser.add_argument(
            '--agent-id', default='',
            help='Aily agent id (defaults to AILY_DEFAULT_AGENT_ID)')
        parser.add_argument(
            '--list', action='store_true',
            help='List applications with their bound agent and exit')
        parser.add_argument(
            '--default', action='store_true',
            help='Also mark this application as the workspace main agent')

    def handle(self, *args, **options):
        if options['list']:
            self._list()
            return

        provider, _ = Provider.objects.update_or_create(
            key='feishu_aily',
            defaults={
                'name': 'Feishu Aily',
                'description': '飞书 Aily 自定义智能体 Runtime Provider',
                'supported_runtime_types': ['agent', 'workflow'],
                'capabilities': AILY_AGENT_CAPABILITIES,
                'start_rate_limit': '10/s',
                'max_inflight': settings.AILY_MAX_INFLIGHT,
                'poll_rate_limit': 'special',
                'artifact_rate_limit': '50/s',
                'timeout_seconds': settings.AILY_STREAM_TIMEOUT_SECONDS,
                'retry_policy': {
                    'max_attempts': 3,
                    'backoff_seconds': settings.AILY_POLL_BACKOFF_SECONDS,
                },
                'secret_ref': 'FEISHU_APP_SECRET',
                'base_url': settings.AILY_BASE_URL,
                'status': Provider.Status.ACTIVE,
            },
        )

        agent_id = options['agent_id'] or settings.AILY_DEFAULT_AGENT_ID
        if not agent_id:
            self.stdout.write(self.style.WARNING(
                'Provider created; no AILY_DEFAULT_AGENT_ID, binding skipped'))
            return

        application = Application.objects.filter(
            slug=options['application_slug']).first()
        if application is None:
            raise CommandError(
                f'application {options["application_slug"]!r} not found '
                f'(use --list to see the existing slugs, or create the '
                f'application in Django Admin first)')

        binding, created = ApplicationRuntimeBinding.objects.update_or_create(
            application=application,
            runtime_type=RuntimeType.AGENT,
            provider_key=provider.key,
            defaults={
                'provider': provider,
                'external_resource_id': agent_id,
                'identity_mode': IdentityMode.USER,
                'execution_mode': ExecutionMode.INTERACTIVE,
                'session_policy': SessionPolicy.LAZY,
                'artifact_policy': ArtifactPolicy.EXTERNAL_REFRESH,
                'capabilities': AILY_AGENT_CAPABILITIES,
                'timeout_seconds': settings.AILY_STREAM_TIMEOUT_SECONDS,
                'enabled': True,
            },
        )
        action = 'created' if created else 'updated'
        self.stdout.write(self.style.SUCCESS(
            f'{action} {application.slug} -> feishu_aily:{agent_id}'))

        if options['default']:
            try:
                Application.set_default_agent(application)
            except ValidationError as exc:
                raise CommandError('；'.join(exc.messages)) from exc
            self.stdout.write(self.style.SUCCESS(
                f'{application.slug} is now the workspace main agent'))

    def _list(self):
        self.stdout.write('applications:')
        for application in Application.objects.order_by('id').select_related(
                'category'):
            bindings = application.runtime_bindings.select_related(
                'provider').order_by('created_at')
            if bindings:
                described = ', '.join(
                    f'{b.provider_key}/{b.runtime_type}:'
                    f'{b.external_resource_id or "-"}'
                    f'{"(disabled)" if not b.enabled else ""}'
                    for b in bindings)
            else:
                described = '（无绑定：无法发起 Run）'
            mark = ' ← 主智能体' if application.is_default_agent else ''
            self.stdout.write(
                f'  [{application.pk}] {application.slug} '
                f'({application.name}, kind={application.kind}, '
                f'public={application.is_public}) {described}{mark}')
        if not Application.default_agent():
            self.stdout.write(self.style.WARNING(
                '  no main agent set: the workspace falls back to the first '
                'bound chat application. Set one with --default or in '
                'Django Admin.'))
