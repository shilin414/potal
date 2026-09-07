from rest_framework import serializers
from .models import TemplateReview, TemplateComment, UserFavorite
from apps.templates.serializers import TemplateListSerializer

class TemplateReviewSerializer(serializers.ModelSerializer):
    user_username = serializers.CharField(source='user.username', read_only=True)
    template_title = serializers.CharField(source='template.title', read_only=True)

    class Meta:
        model = TemplateReview
        fields = ['id', 'template', 'template_title', 'user', 'user_username', 'rating', 'comment', 'created_at']

class TemplateCommentSerializer(serializers.ModelSerializer):
    user_username = serializers.CharField(source='user.username', read_only=True)
    replies = serializers.SerializerMethodField()

    class Meta:
        model = TemplateComment
        fields = ['id', 'template', 'user', 'user_username', 'content', 'parent', 'replies', 'created_at']

    def get_replies(self, obj):
        replies = obj.replies.all()[:5]
        return TemplateCommentSerializer(replies, many=True).data

class UserFavoriteSerializer(serializers.ModelSerializer):
    template = TemplateListSerializer(read_only=True)

    class Meta:
        model = UserFavorite
        fields = ['id', 'template', 'created_at']