from django.urls import path, include
from rest_framework.routers import DefaultRouter
from .views import TemplateReviewViewSet, TemplateCommentViewSet, UserFavoriteViewSet, MarketViewSet

router = DefaultRouter()
router.register(r'reviews', TemplateReviewViewSet, basename='template_review')
router.register(r'comments', TemplateCommentViewSet, basename='template_comment')
router.register(r'favorites', UserFavoriteViewSet, basename='user_favorite')
router.register(r'', MarketViewSet, basename='market')

urlpatterns = [
    path('', include(router.urls)),
]