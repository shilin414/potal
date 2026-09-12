import json
import time
import uuid

from django.utils.deprecation import MiddlewareMixin

from .models import AuditLog


class RequestContextMiddleware(MiddlewareMixin):
    """Attach correlation id and persist mutation audit records."""

    def process_request(self, request):
        request.request_id = request.headers.get('X-Request-ID') or uuid.uuid4().hex
        request._audit_started_at = time.monotonic()

    def process_response(self, request, response):
        response['X-Request-ID'] = getattr(request, 'request_id', '')
        if request.method not in ('GET', 'HEAD', 'OPTIONS'):
            user = getattr(request, 'user', None)
            if user and user.is_authenticated:
                organization = getattr(request, 'organization', None)
                if organization is None:
                    membership = user.organization_memberships.filter(
                        is_active=True).select_related('organization').first()
                    organization = membership.organization if membership else None
                forwarded = request.META.get('HTTP_X_FORWARDED_FOR', '')
                ip_address = forwarded.split(',')[0].strip() if forwarded else \
                    request.META.get('REMOTE_ADDR')
                try:
                    AuditLog.objects.create(
                        organization=organization,
                        actor=user,
                        action=f'{request.method.lower()}:{request.resolver_match.route if request.resolver_match else request.path}',
                        resource_type=(request.resolver_match.app_name if request.resolver_match else ''),
                        resource_id=str((request.resolver_match.kwargs or {}).get('pk', ''))
                            if request.resolver_match else '',
                        request_id=getattr(request, 'request_id', ''),
                        ip_address=ip_address or None,
                        user_agent=request.headers.get('User-Agent', '')[:1000],
                        metadata={
                            'path': request.path,
                            'status_code': response.status_code,
                            'duration_ms': int((time.monotonic() - request._audit_started_at) * 1000),
                        },
                    )
                except Exception:
                    # Audit failure must not turn a successful business request into 500.
                    pass
        return response
