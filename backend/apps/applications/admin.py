"""
Admin configuration for applications app.
"""
from django.contrib import admin
from apps.applications.models import (
    Application, ApplicationAgentBinding, ApplicationCategory,
    ApplicationSkillBinding, ChatApplicationProfile,
    GuidedOption, GuidedPrompt, GuidedQuestion, Skill,
)


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
    list_display = ['name', 'category', 'slug', 'is_public', 'created_by', 'created_at', 'updated_at']
    list_filter = ['category', 'is_public', 'created_at']
    search_fields = ['name', 'description', 'slug']
    ordering = ['category__order', 'name']
    readonly_fields = ['created_at', 'updated_at']


admin.site.register(ChatApplicationProfile)
admin.site.register(ApplicationAgentBinding)
admin.site.register(ApplicationSkillBinding)
admin.site.register(Skill)
admin.site.register(GuidedPrompt)
admin.site.register(GuidedQuestion)
admin.site.register(GuidedOption)
