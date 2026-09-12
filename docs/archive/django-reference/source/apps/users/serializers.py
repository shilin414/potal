"""
Serializers for users app.
"""
from rest_framework import serializers
from django.contrib.auth import get_user_model
from django.contrib.auth.password_validation import validate_password

from .display import display_identity

User = get_user_model()


class UserSerializer(serializers.ModelSerializer):
    """用户序列化器。

    带上聊天展示身份（``display_name`` / ``display_id``，见 apps.users.display）：
    前端把每条消息的发送方渲染成 ``姓名（user_id）``。
    """

    display_name = serializers.SerializerMethodField()
    display_id = serializers.SerializerMethodField()

    class Meta:
        model = User
        fields = ['id', 'username', 'email', 'role', 'avatar', 'bio',
                  'display_name', 'display_id', 'created_at']
        read_only_fields = ['id', 'role', 'created_at']

    def get_display_name(self, obj) -> str:
        return display_identity(obj)['display_name']

    def get_display_id(self, obj) -> str:
        return display_identity(obj)['display_id']


class UserDetailSerializer(serializers.ModelSerializer):
    """用户详情序列化器"""

    display_name = serializers.SerializerMethodField()
    display_id = serializers.SerializerMethodField()

    class Meta:
        model = User
        fields = ['id', 'username', 'email', 'role', 'avatar', 'bio',
                  'display_name', 'display_id', 'created_at', 'updated_at']
        read_only_fields = ['id', 'role', 'created_at', 'updated_at']

    def get_display_name(self, obj) -> str:
        return display_identity(obj)['display_name']

    def get_display_id(self, obj) -> str:
        return display_identity(obj)['display_id']


class RegisterSerializer(serializers.ModelSerializer):
    """注册序列化器"""
    password = serializers.CharField(
        write_only=True,
        required=True,
        validators=[validate_password],
        style={'input_type': 'password'}
    )
    password_confirm = serializers.CharField(
        write_only=True,
        required=True,
        style={'input_type': 'password'}
    )

    class Meta:
        model = User
        fields = ['username', 'email', 'password', 'password_confirm']

    def validate(self, attrs):
        if attrs['password'] != attrs['password_confirm']:
            raise serializers.ValidationError({'password_confirm': '两次输入的密码不一致'})
        return attrs

    def create(self, validated_data):
        validated_data.pop('password_confirm')
        password = validated_data.pop('password')
        user = User(**validated_data)
        user.set_password(password)
        user.save()
        return user


class LoginSerializer(serializers.Serializer):
    """登录序列化器"""
    username = serializers.CharField(required=True)
    password = serializers.CharField(
        required=True,
        write_only=True,
        style={'input_type': 'password'}
    )


class ChangePasswordSerializer(serializers.Serializer):
    """修改密码序列化器"""
    old_password = serializers.CharField(required=True, write_only=True)
    new_password = serializers.CharField(
        required=True,
        write_only=True,
        validators=[validate_password]
    )
    new_password_confirm = serializers.CharField(required=True, write_only=True)

    def validate(self, attrs):
        if attrs['new_password'] != attrs['new_password_confirm']:
            raise serializers.ValidationError({'new_password_confirm': '两次输入的密码不一致'})
        return attrs
