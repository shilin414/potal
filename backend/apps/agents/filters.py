"""Filters for agents app."""
import django_filters
from .models import Agent


class AgentFilter(django_filters.FilterSet):
    """智能体过滤器"""
    category = django_filters.CharFilter(
        field_name='category__slug',
        lookup_expr='iexact',
        help_text='按分类 slug 过滤',
    )
    category_name = django_filters.CharFilter(
        field_name='category__name',
        lookup_expr='icontains',
        help_text='按分类名称搜索',
    )

    class Meta:
        model = Agent
        fields = ['category', 'category_name']
