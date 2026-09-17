/**
 * Legacy `/apps/:id/run` deep links → the in-shell application workspace (§32).
 *
 * The URL may carry either the numeric id or the slug (the app-history sidebar
 * uses slugs), so resolve that ONE application (执行报告 §12, P1-2) and hand
 * off to `/chat/:slug` or `/app/:slug` depending on its kind. The query string
 * (`?conversation=`) is preserved so a restored run still opens.
 *
 * Resolution is cache-then-resolve-by-id/slug: never a catalog download.
 *
 * One exception: project-scoped application workspaces (`?workspace=<id>`
 * from the app-history rail) still need the Project/file-panel surface, which
 * has no Application model yet — those keep the legacy runtime page until the
 * project↔application ownership is modelled. See 开发进度清单 P1.
 */
import { useEffect, useState } from 'react';
import { Navigate, useLocation, useParams } from 'react-router-dom';
import { Spin } from 'antd';
import ApplicationRuntimePage from '@/pages/Apps/ApplicationRuntimePage';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import type { V2Application } from '@/services/runApi';

const LegacyAppRunRedirect: React.FC = () => {
  const { id } = useParams<{ id: string }>();
  const location = useLocation();
  const byId = useApplicationEntityStore((state) => state.byId);
  const bySlug = useApplicationEntityStore((state) => state.bySlug);
  const ensure = useApplicationEntityStore((state) => state.ensure);
  const ensureBySlug = useApplicationEntityStore((state) => state.ensureBySlug);
  const openApplication = useWorkspaceStore((state) => state.openApplication);
  const [resolving, setResolving] = useState(true);

  const projectScoped = new URLSearchParams(location.search).has('workspace');
  // The URL is either a numeric id or a slug; the cache keys reflect that.
  const numeric = id == null ? NaN : Number(id);
  const isNumeric = !Number.isNaN(numeric);
  const application: V2Application | undefined = id == null
    ? undefined
    : (isNumeric ? byId[numeric] : bySlug[id]);

  useEffect(() => {
    if (projectScoped || id == null) {
      setResolving(false);
      return undefined;
    }
    let live = true;
    setResolving(true);
    const lookup = isNumeric ? ensure(numeric) : ensureBySlug(id);
    void lookup.finally(() => { if (live) setResolving(false); });
    return () => { live = false; };
  }, [id, numeric, isNumeric, projectScoped, ensure, ensureBySlug]);

  useEffect(() => {
    if (application) openApplication(application.id);
  }, [application, openApplication]);

  if (projectScoped) return <ApplicationRuntimePage />;

  if (!application) {
    if (resolving) {
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
