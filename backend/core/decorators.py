"""
Decorators for the project.
"""
from functools import wraps
from rest_framework.response import Response
from rest_framework import status


def require_role(*roles):
    """角色权限装饰器"""
    def decorator(view_func):
        @wraps(view_func)
        def wrapped_view(request, *args, **kwargs):
            if not request.user.is_authenticated:
                return Response(
                    {'detail': '需要登录'},
                    status=status.HTTP_401_UNAUTHORIZED
                )
            if request.user.role not in roles:
                return Response(
                    {'detail': '权限不足'},
                    status=status.HTTP_403_FORBIDDEN
                )
            return view_func(request, *args, **kwargs)
        return wrapped_view
    return decorator
