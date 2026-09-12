"""The /api/v2/ URL namespace.

Read/execution endpoints live in ``apps.execution.views`` (Runs, events,
SSE, artifacts, the application listing). Authoring endpoints — 新建智能体、
头像、主智能体、运行时目录 — live in ``apps.applications.v2_authoring``
because they own the catalog, not the execution plane.

Both lists are aggregated here so ``/api/v2/`` stays a single include in
``backend/urls.py``.
"""
from apps.applications.v2_authoring import urlpatterns as authoring_urlpatterns
from apps.execution.views import urlpatterns as execution_urlpatterns

urlpatterns = execution_urlpatterns + authoring_urlpatterns
