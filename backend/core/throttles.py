import time

from django.core.cache import cache
from rest_framework.throttling import BaseThrottle


class OrganizationRateThrottle(BaseThrottle):
    """Dynamic per-organization request limit sourced from QuotaPolicy."""

    duration = 60

    def allow_request(self, request, view):
        user = request.user
        if not user or not user.is_authenticated:
            return True
        from apps.enterprise.permissions import resolve_organization
        organization = resolve_organization(request, required=False)
        if organization is None:
            return True
        quota = getattr(organization, 'quota_policy', None)
        limit = quota.requests_per_minute if quota else 120
        key = f'org-rate:{organization.id}:{int(time.time() // self.duration)}'
        try:
            count = 1 if cache.add(key, 1, timeout=self.duration + 5) else cache.incr(key)
        except (ValueError, NotImplementedError):
            return True
        return count <= limit

    def wait(self):
        return self.duration
