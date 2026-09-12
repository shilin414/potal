"""
Admin configuration for applications app.
"""
from django.contrib import admin, messages
from django.core.exceptions import ValidationError

from apps.applications.models import (
    Application, ApplicationAgentBinding, ApplicationCategory,
    ApplicationFavorite, ApplicationSkillBinding, ChatApplicationProfile,
    GuidedOption, GuidedPrompt, GuidedQuestion, Skill,
)


class ApplicationRuntimeBindingInline(admin.StackedInline):
    """Wire an application to a provider resource from the application page.

    For Aily this is where the custom agent's `agent_id` lives, so adding
    another Aily agent = create the Application here + fill
    `external_resource_id` — no code change and no management command needed.
    """
    from apps.catalog.models import ApplicationRuntimeBinding as _Binding
    model = _Binding
    extra = 0
    fk_name = 'application'
    autocomplete_fields = ['provider']
    fields = ['enabled', 'runtime_type', 'provider', 'provider_key',
              'external_resource_id', 'identity_mode', 'execution_mode',
              'session_policy', 'artifact_policy', 'timeout_seconds', 'config']
    verbose_name = '运行时绑定'
    verbose_name_plural = '运行时绑定（Application → Provider 资源）'


@admin.register(ApplicationCategory)
class ApplicationCategoryAdmin(admin.ModelAdmin):
    """Admin interface for ApplicationCategory model."""
    list_display = ['name', 'slug', 'order']
    list_filter = ['order']
    search_fields = ['name', 'description']
    ordering = ['order']


@admin.register(Application)
class ApplicationAdmin(admin.ModelAdmin):
    """Admin interface for Application model."""
    list_display = ['name', 'category', 'slug', 'kind', 'is_public',
                    'is_default_agent', 'created_by', 'created_at']
    list_filter = ['category', 'kind', 'is_public', 'is_default_agent',
                   'created_at']
    search_fields = ['name', 'description', 'slug']
    ordering = ['category__order', 'name']
    readonly_fields = ['created_at', 'updated_at']
    inlines = [ApplicationRuntimeBindingInline]
    actions = ['make_default_agent']
    fieldsets = [
        ('基础', {
            'fields': ['category', 'name', 'slug', 'description', 'icon',
                       'color', 'tags', 'developer', 'screenshots'],
        }),
        ('类型与渲染', {
            'fields': ['kind', 'renderer_key', 'executor_key'],
            'description': 'kind=chat 的应用走统一 Run 聊天；固定页面用 '
                           'renderer_key 交给前端 Renderer Registry。',
        }),
        ('工作台默认主智能体', {
            'fields': ['is_default_agent'],
            'description': '勾选表示它是工作台的默认主智能体：首页输入框默认'
                           '发给它，无法解析的 @ 也落回它。只会有一个应用持有'
                           '该标记（保存时自动清掉其他应用）。',
        }),
        ('归属', {'fields': ['is_public', 'organization', 'created_by']}),
        ('schema / 配置', {
            'fields': ['input_schema', 'output_schema', 'default_config'],
            'classes': ['collapse'],
        }),
        ('时间', {'fields': ['created_at', 'updated_at']}),
    ]

    @admin.action(description='设为主智能体（工作台默认）')
    def make_default_agent(self, request, queryset):
        if queryset.count() != 1:
            self.message_user(request, '请只选择一个应用', level=messages.ERROR)
            return
        application = queryset.first()
        try:
            Application.set_default_agent(application)
        except ValidationError as exc:
            self.message_user(
                request, '；'.join(exc.messages), level=messages.ERROR)
            return
        self.message_user(
            request, f'{application.name} 已设为默认主智能体',
            level=messages.SUCCESS)

    def save_model(self, request, obj, form, change):
        """Ticking the flag keeps it unique; un-ticking leaves none."""
        super().save_model(request, obj, form, change)
        if obj.is_default_agent:
            try:
                Application.set_default_agent(obj)
            except ValidationError as exc:
                Application.objects.filter(pk=obj.pk).update(
                    is_default_agent=False)
                self.message_user(
                    request, '；'.join(exc.messages), level=messages.ERROR)
        elif change and 'is_default_agent' in form.changed_data:
            Application.objects.filter(pk=obj.pk).update(is_default_agent=False)


admin.site.register(ChatApplicationProfile)
admin.site.register(ApplicationAgentBinding)
admin.site.register(ApplicationSkillBinding)
admin.site.register(ApplicationFavorite)
admin.site.register(Skill)
admin.site.register(GuidedPrompt)
admin.site.register(GuidedQuestion)
admin.site.register(GuidedOption)
