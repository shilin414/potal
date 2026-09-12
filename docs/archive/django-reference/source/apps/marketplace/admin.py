from django.contrib import admin
from apps.marketplace.models import TemplateReview, TemplateComment, UserFavorite


@admin.register(TemplateReview)
class TemplateReviewAdmin(admin.ModelAdmin):
    list_display = ['template', 'user', 'rating', 'created_at']
    list_filter = ['rating', 'created_at']
    search_fields = ['template__title', 'user__username', 'comment']
    ordering = ['-created_at']
    readonly_fields = ['created_at', 'updated_at']


@admin.register(TemplateComment)
class TemplateCommentAdmin(admin.ModelAdmin):
    list_display = ['template', 'user', 'content', 'created_at']
    list_filter = ['created_at']
    search_fields = ['template__title', 'user__username', 'content']
    ordering = ['-created_at']
    readonly_fields = ['created_at', 'updated_at']


@admin.register(UserFavorite)
class UserFavoriteAdmin(admin.ModelAdmin):
    list_display = ['user', 'template', 'created_at']
    list_filter = ['created_at']
    search_fields = ['user__username', 'template__title']
    ordering = ['-created_at']
    readonly_fields = ['created_at']
