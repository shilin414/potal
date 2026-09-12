"""
Admin configuration for users app.
"""
from django.contrib import admin
from django.contrib.auth.admin import UserAdmin as BaseUserAdmin
from django.contrib.auth import get_user_model

User = get_user_model()


@admin.register(User)
class UserAdmin(BaseUserAdmin):
    """
    Admin interface for User model.
    """
    list_display = ['username', 'display_id', 'email', 'first_name', 'last_name',
                    'auth_source', 'is_staff', 'is_active']
    list_filter = ['is_staff', 'is_active', 'is_superuser', 'auth_source']
    search_fields = ['username', 'display_id', 'email', 'first_name', 'last_name']
    ordering = ['username']
    # display_id 是聊天里「姓名（user_id）」括号里的那串数字；留空则回退
    # FeishuIdentity.feishu_user_id，再回退本地主键。
    fieldsets = BaseUserAdmin.fieldsets + (
        ('Studio 展示信息', {'fields': ('display_id', 'avatar', 'role', 'auth_source')}),
    )
