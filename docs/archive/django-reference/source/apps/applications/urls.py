from django.urls import path, include
from rest_framework.routers import DefaultRouter
from .views import (
    ApplicationCategoryViewSet,
    ApplicationViewSet,
    SkillViewSet,
    generate_image,
)
from .runtime_skill_views import (
    RuntimeSkillCollectionView,
    RuntimeSkillDetailView,
)

router = DefaultRouter()
router.register(r'categories', ApplicationCategoryViewSet, basename='application_category')
router.register(r'skills', SkillViewSet, basename='skill')
router.register(r'', ApplicationViewSet, basename='application')

urlpatterns = [
    # Explicit path BEFORE the router include so it wins over /apps/<slug>/.
    path('image-generate/', generate_image),
    path('runtime-skills/', RuntimeSkillCollectionView.as_view(),
         name='runtime-skill-list'),
    path('runtime-skills/<str:provider>/<str:slug>/',
         RuntimeSkillDetailView.as_view(), name='runtime-skill-detail'),
    path('', include(router.urls)),
]
