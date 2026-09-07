from django.contrib import admin

from apps.templates.models import Template, TemplateAnalysisSection, TemplateCategory


@admin.register(TemplateCategory)
class TemplateCategoryAdmin(admin.ModelAdmin):
    list_display = ['name', 'slug', 'order']
    ordering = ['order']


class TemplateAnalysisSectionInline(admin.TabularInline):
    model = TemplateAnalysisSection
    extra = 0
    ordering = ['order']


@admin.register(Template)
class TemplateAdmin(admin.ModelAdmin):
    list_display = [
        'title', 'category', 'content_type', 'source_platform', 'status',
        'is_featured', 'view_count', 'updated_at',
    ]
    list_filter = [
        'category', 'content_type', 'copyright_mode', 'status', 'is_featured',
    ]
    search_fields = [
        'title', 'summary', 'source_title', 'source_author', 'source_platform',
    ]
    ordering = ['-is_featured', '-updated_at']
    readonly_fields = ['view_count', 'created_at', 'updated_at']
    inlines = [TemplateAnalysisSectionInline]


@admin.register(TemplateAnalysisSection)
class TemplateAnalysisSectionAdmin(admin.ModelAdmin):
    list_display = ['template', 'section_type', 'title', 'order']
    list_filter = ['section_type']
    search_fields = ['template__title', 'title', 'content', 'evidence_quote']
    ordering = ['template', 'order']
