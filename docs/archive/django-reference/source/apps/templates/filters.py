"""Filters for the content case library."""
import django_filters

from .models import Template


class TemplateFilter(django_filters.FilterSet):
    category = django_filters.CharFilter(
        field_name='category__slug', lookup_expr='iexact')
    category_name = django_filters.CharFilter(
        field_name='category__name', lookup_expr='icontains')
    content_type = django_filters.CharFilter(
        field_name='content_type', lookup_expr='iexact')
    platform = django_filters.CharFilter(
        field_name='source_platform', lookup_expr='icontains')
    copyright_mode = django_filters.CharFilter(
        field_name='copyright_mode', lookup_expr='iexact')
    tag = django_filters.CharFilter(method='filter_tag')

    class Meta:
        model = Template
        fields = [
            'category', 'category_name', 'content_type', 'platform',
            'copyright_mode', 'tag',
        ]

    def filter_tag(self, queryset, name, value):
        # JSON containment is not consistently available on SQLite. Resolve
        # matching ids in Python, then return a QuerySet so pagination remains valid.
        matching_ids = [item.id for item in queryset if value in (item.tags or [])]
        return queryset.filter(id__in=matching_ids)
