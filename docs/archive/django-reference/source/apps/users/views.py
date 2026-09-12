"""
Views for users app.
"""
from rest_framework import status, generics
from rest_framework.decorators import api_view, permission_classes
from rest_framework.permissions import AllowAny, IsAuthenticated
from rest_framework.response import Response
from rest_framework_simplejwt.tokens import RefreshToken
from rest_framework_simplejwt.views import TokenObtainPairView, TokenRefreshView
from django.conf import settings
from django.contrib.auth import authenticate
from django.utils import timezone
from django.utils.dateparse import parse_datetime
from .serializers import (
    UserSerializer,
    UserDetailSerializer,
    RegisterSerializer,
    LoginSerializer,
    ChangePasswordSerializer
)
from .models import User
from .models import UserAPIKey


class RegisterView(generics.CreateAPIView):
    """用户注册"""
    queryset = User.objects.all()
    permission_classes = [AllowAny]
    serializer_class = RegisterSerializer

    def create(self, request, *args, **kwargs):
        serializer = self.get_serializer(data=request.data)
        serializer.is_valid(raise_exception=True)
        user = serializer.save()

        # 生成 JWT token
        refresh = RefreshToken.for_user(user)

        return Response({
            'user': UserSerializer(user).data,
            'tokens': {
                'refresh': str(refresh),
                'access': str(refresh.access_token),
            }
        }, status=status.HTTP_201_CREATED)


class CustomTokenObtainPairView(TokenObtainPairView):
    """自定义登录视图"""
    permission_classes = [AllowAny]
    serializer_class = LoginSerializer

    def post(self, request, *args, **kwargs):
        serializer = self.get_serializer(data=request.data)
        serializer.is_valid(raise_exception=True)

        username = serializer.validated_data['username']
        password = serializer.validated_data['password']

        user = authenticate(username=username, password=password)

        if user is None:
            return Response(
                {'detail': '用户名或密码错误'},
                status=status.HTTP_401_UNAUTHORIZED
            )

        refresh = RefreshToken.for_user(user)

        response = Response({
            'user': UserSerializer(user).data,
            'tokens': {
                'refresh': str(refresh),
                'access': str(refresh.access_token),
            }
        })
        # Also issue the studio_session cookie so same-origin navigations
        # (e.g. window.open /api/v2/artifacts/{id}/open) authenticate without
        # a bearer header. The Feishu OAuth exchange sets the same cookie.
        from apps.identity.services import SESSION_MAX_AGE, sign_session
        response.set_cookie(
            'studio_session', sign_session(user.pk),
            max_age=SESSION_MAX_AGE, httponly=True, samesite='Lax',
            secure=not settings.DEBUG)
        return response


class UserProfileView(generics.RetrieveUpdateAPIView):
    """用户信息"""
    serializer_class = UserDetailSerializer
    permission_classes = [IsAuthenticated]

    def get_object(self):
        return self.request.user


class ChangePasswordView(generics.UpdateAPIView):
    """修改密码"""
    serializer_class = ChangePasswordSerializer
    permission_classes = [IsAuthenticated]

    def get_object(self):
        return self.request.user

    def update(self, request, *args, **kwargs):
        serializer = self.get_serializer(data=request.data)
        serializer.is_valid(raise_exception=True)

        user = self.get_object()
        old_password = serializer.validated_data['old_password']
        new_password = serializer.validated_data['new_password']

        if not user.check_password(old_password):
            return Response(
                {'old_password': '旧密码错误'},
                status=status.HTTP_400_BAD_REQUEST
            )

        user.set_password(new_password)
        user.save()

        return Response({'detail': '密码修改成功'})


class GenerateApiKeyView(generics.GenericAPIView):
    """生成 API 密钥"""
    permission_classes = [IsAuthenticated]

    def post(self, request, *args, **kwargs):
        name = str(request.data.get('name') or 'default')[:100]
        scopes = request.data.get('scopes') or []
        if not isinstance(scopes, list):
            return Response({'scopes': 'Must be a list.'}, status=status.HTTP_400_BAD_REQUEST)
        expires_at = request.data.get('expires_at')
        if expires_at:
            expires_at = parse_datetime(str(expires_at))
            if expires_at is None or expires_at <= timezone.now():
                return Response({'expires_at': 'Use a future ISO-8601 datetime.'},
                                status=status.HTTP_400_BAD_REQUEST)
        key, raw_key = UserAPIKey.issue(
            user=request.user, name=name, scopes=scopes, expires_at=expires_at)
        return Response({
            'id': str(key.id),
            'name': key.name,
            'prefix': key.prefix,
            'scopes': key.scopes,
            'expires_at': key.expires_at,
            'api_key': raw_key,
        }, status=status.HTTP_201_CREATED)


class ApiKeyListView(generics.GenericAPIView):
    permission_classes = [IsAuthenticated]

    def get(self, request):
        return Response([{
            'id': str(key.id), 'name': key.name, 'prefix': key.prefix,
            'scopes': key.scopes, 'expires_at': key.expires_at,
            'last_used_at': key.last_used_at, 'revoked_at': key.revoked_at,
            'created_at': key.created_at,
        } for key in request.user.api_keys.all()])


class ApiKeyRevokeView(generics.GenericAPIView):
    permission_classes = [IsAuthenticated]

    def post(self, request, key_id):
        from django.utils import timezone
        key = request.user.api_keys.filter(id=key_id, revoked_at__isnull=True).first()
        if key is None:
            return Response({'detail': 'API key not found.'}, status=404)
        key.revoked_at = timezone.now()
        key.save(update_fields=['revoked_at'])
        return Response(status=status.HTTP_204_NO_CONTENT)


@api_view(['POST'])
@permission_classes([AllowAny])
def logout_view(request):
    """登出

    AllowAny: logout must work even when the access token has expired — the
    axios interceptor calls this from its 401 handler, at which point the
    access token is already dead. We only need the refresh token from the body
    to blacklist it.
    """
    try:
        refresh_token = request.data.get('refresh')
        if refresh_token:
            token = RefreshToken(refresh_token)
            token.blacklist()
        response = Response({'detail': '登出成功'})
        # Drop the studio_session cookie together with the JWTs so same-origin
        # navigations stop authenticating after logout.
        response.delete_cookie('studio_session')
        return response
    except Exception:
        return Response({'detail': '登出失败'}, status=status.HTTP_400_BAD_REQUEST)
