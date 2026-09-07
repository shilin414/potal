from django.contrib import admin
from apps.projects.models import Project, ProjectAsset


@admin.register(Project)
class ProjectAdmin(admin.ModelAdmin):
    list_display = ['title', 'user', 'status', 'application', 'created_at', 'updated_at']
    list_filter = ['status', 'created_at']
    search_fields = ['title', 'description', 'user__username']
    ordering = ['-updated_at']
    readonly_fields = ['created_at', 'updated_at']


@admin.register(ProjectAsset)
class ProjectAssetAdmin(admin.ModelAdmin):
    list_display = ['name', 'project', 'asset_type', 'order', 'created_at']
    list_filter = ['asset_type', 'created_at']
    search_fields = ['name', 'project__title']
    ordering = ['order', 'created_at']
    readonly_fields = ['created_at']
