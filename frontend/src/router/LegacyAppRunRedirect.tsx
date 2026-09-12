/**
 * Legacy `/apps/:id/run` deep links → the in-shell application workspace (§32).
 *
 * The URL may carry either the numeric id or the slug (the app-history sidebar
 * uses slugs), so resolve through the catalog and hand off to
 * `/chat/:slug` or `/app/:slug` depending on the application kind. The query
 * string (`?conversation=`) is preserved so a restored run still opens.
 *
 * One exception: project-scoped application workspaces (`?workspace=<id>`
 * from the app-history rail) still need the Project/file-panel surface, which
 * has no Application model yet — those keep the legacy runtime page until the
 * project↔application ownership is modelled. See 开发进度清单 P1.
 */
import { useEffect } from 'react';
import { Navigate, useLocation, useParams } from 'react-router-dom';
import { Spin } from 'antd';
import ApplicationRuntimePage from '@/pages/Apps/ApplicationRuntimePage';
import { useApplicationCatalogStore } from '@/stores/useApplicationCatalogStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';

const LegacyAppRunRedirect: React.FC = () => {
  const { id } = useParams<{ id: string }>();
  const location = useLocation();
  const applications = useApplicationCatalogStore((state) => state.applications);
  const isLoading = useApplicationCatalogStore((state) => state.isLoading);
  const load = useApplicationCatalogStore((state) => state.load);
  const openApplication = useWorkspaceStore((state) => state.openApplication);

  useEffect(() => { void load(); }, [load]);

  const projectScoped = new URLSearchParams(location.search).has('workspace');
  const application = applications.find((app) => (
    app.slug === id || String(app.id) === id));

  useEffect(() => {
    if (application) openApplication(application.id);
  }, [application, openApplication]);

  if (projectScoped) return <ApplicationRuntimePage />;

  if (!application) {
    if (isLoading) {
      return <div className="workspace-host__loading"><Spin size="large" /></div>;
    }
    return <Navigate to="/" replace />;
  }

  const base = application.kind === 'chat'
    ? `/chat/${application.slug}`
    : `/app/${application.slug}`;
  return <Navigate to={`${base}${location.search}`} replace />;
};

export default LegacyAppRunRedirect;
