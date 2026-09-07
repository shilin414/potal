"""URL routes for app_runner REST endpoints."""
from django.urls import path

from .views import create_job, job_detail, list_dir, scan_folder, stop_job

urlpatterns = [
    path('jobs/', create_job),
    path('jobs/<uuid:id>/', job_detail),
    path('jobs/<uuid:id>/stop/', stop_job),
    path('fs/scan/', scan_folder),
    path('fs/list/', list_dir),
]
