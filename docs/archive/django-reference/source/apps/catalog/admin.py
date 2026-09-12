"""Admin for the runtime catalog (Provider / ApplicationRuntimeBinding).

This is the supported configuration surface for wiring an Application to a
concrete provider resource — for Aily that means pasting the custom agent's
`agent_id` into a binding, or switching an application to another agent,
without touching code or the shell.

Credentials are never edited here: `secret_ref` only names an environment
secret (`backend/.env`), so the admin never stores or displays a token.
"""
from django.contrib import admin

from apps.catalog.models import ApplicationRuntimeBinding, Provider


@admin.register(Provider)
class ProviderAdmin(admin.ModelAdmin):
    list_display = ['key', 'name', 'supported_runtime_types', 'status',
                    'start_rate_limit', 'max_inflight', 'secret_ref']
    list_filter = ['status']
    search_fields = ['key', 'name']
    readonly_fields = ['created_at', 'updated_at']
    fieldsets = [
        ('基础', {
            'fields': ['key', 'name', 'description', 'status'],
            'description': 'provider_key 是业务层唯一允许的 Provider 标识，'
                           '业务代码不得出现 if provider == "..." 分支。',
        }),
        ('能力与限流', {
            'fields': ['supported_runtime_types', 'capabilities',
                       'start_rate_limit', 'max_inflight', 'poll_rate_limit',
                       'artifact_rate_limit', 'timeout_seconds',
                       'retry_policy', 'circuit_breaker'],
        }),
        ('连接（不含凭据本体）', {
            'fields': ['base_url', 'secret_ref'],
            'description': 'secret_ref 只写环境变量名（如 FEISHU_APP_SECRET），'
                           '真实密钥只存在于 backend/.env。',
        }),
        ('时间', {'fields': ['created_at', 'updated_at']}),
    ]


@admin.register(ApplicationRuntimeBinding)
class ApplicationRuntimeBindingAdmin(admin.ModelAdmin):
    list_display = ['application', 'runtime_type', 'provider_key',
                    'external_resource_id', 'identity_mode', 'execution_mode',
                    'enabled', 'updated_at']
    list_filter = ['provider_key', 'runtime_type', 'identity_mode',
                   'execution_mode', 'enabled']
    search_fields = ['application__name', 'application__slug',
                     'external_resource_id']
    autocomplete_fields = ['application']
    list_select_related = ['application', 'provider']
    readonly_fields = ['id', 'created_at', 'updated_at']
    fieldsets = [
        ('归属', {
            'fields': ['id', 'application', 'enabled'],
        }),
        ('运行时', {
            'fields': ['runtime_type', 'provider', 'provider_key'],
            'description': '改 provider_key 前先在 Provider 表里建好对应行。',
        }),
        ('外部资源（Aily 智能体）', {
            'fields': ['external_resource_id', 'endpoint_key'],
            'description': '飞书 Aily 自定义智能体：这里填 agent_id（形如 '
                           'agent_4jz9pu9exyaws）。换一个绑定的 ID 即等于把该'
                           '应用切换到另一个智能体，改完无需重启 web（worker '
                           '每次按 runtime_snapshot 解析）。',
        }),
        ('身份与执行', {
            'fields': ['identity_mode', 'execution_mode', 'session_policy',
                       'artifact_policy', 'timeout_seconds'],
            'description': 'identity_mode=user 表示用调用者自己的 UAT 调 Aily；'
                           '只有智能体允许应用身份时才用 tenant(TAT)。',
        }),
        ('schema / 配置', {
            'fields': ['capabilities', 'input_schema', 'output_schema',
                       'config', 'secret_ref'],
            'classes': ['collapse'],
        }),
        ('时间', {'fields': ['created_at', 'updated_at']}),
    ]
