/**
 * Legacy `/apps/:id/run` deep links resolve one consume-eligible application
 * before redirecting into the shell workspace. A cached management row is not
 * sufficient admission for the redirect.
 */
import { useEffect, useState } from 'react';
import { Button, Empty, Spin } from 'antd';
import { Navigate, useLocation, useParams } from 'react-router-dom';
import ApplicationRuntimePage from '@/pages/Apps/ApplicationRuntimePage';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import type { V2Application } from '@/services/runApi';

const LegacyAppRunRedirect: React.FC = () => {
  const { id } = useParams<{ id: string }>();
  const location = useLocation();
  const ensure = useApplicationEntityStore((state) => state.ensure);
  const ensureBySlug = useApplicationEntityStore((state) => state.ensureBySlug);
  const openApplication = useWorkspaceStore((state) => state.openApplication);
  const [resolvedApplication, setResolvedApplication] = useState<
    V2Application | null | undefined
  >(undefined);
  const [resolvedLookup, setResolvedLookup] = useState<string | null>(null);
  const [resolveFailed, setResolveFailed] = useState(false);
  const [retryNonce, setRetryNonce] = useState(0);

  const projectScoped = new URLSearchParams(location.search).has('workspace');
  const numeric = id == null ? NaN : Number(id);
  const isNumeric = !Number.isNaN(numeric);

  useEffect(() => {
    if (projectScoped || id == null) {
      setResolvedLookup(null);
      setResolvedApplication(null);
      setResolveFailed(false);
      return undefined;
    }

    let active = true;
    setResolvedLookup(id);
    setResolvedApplication(undefined);
    setResolveFailed(false);
    const lookup = isNumeric
      ? ensure(numeric, { maxAgeMs: 0, bypassBackoff: retryNonce > 0 })
      : ensureBySlug(id, { maxAgeMs: 0, bypassBackoff: retryNonce > 0 });
    void lookup
      .then((application) => {
        if (!active) return;
        setResolvedApplication(application ?? null);
      })
      .catch(() => {
        if (!active) return;
        setResolveFailed(true);
      });
    return () => { active = false; };
  }, [id, numeric, isNumeric, projectScoped, ensure, ensureBySlug, retryNonce]);

  useEffect(() => {
    if (resolvedApplication) openApplication(resolvedApplication.id);
  }, [resolvedApplication, openApplication]);

  if (projectScoped) return <ApplicationRuntimePage />;

  if (resolvedLookup !== id) {
    return <div className="workspace-host__loading"><Spin size="large" /></div>;
  }

  if (resolveFailed) {
    return (
      <div className="workspace-host__missing">
        <Empty
          image={Empty.PRESENTED_IMAGE_SIMPLE}
          description="应用加载失败，请检查网络后重试"
        >
          <Button type="primary" onClick={() => setRetryNonce((value) => value + 1)}>
            重新加载
          </Button>
        </Empty>
      </div>
    );
  }
  if (resolvedApplication === undefined) {
    return <div className="workspace-host__loading"><Spin size="large" /></div>;
  }

  if (resolvedApplication === null) return <Navigate to="/" replace />;

  const base = resolvedApplication.kind === 'chat'
    ? `/chat/${resolvedApplication.slug}`
    : `/app/${resolvedApplication.slug}`;
  return <Navigate to={`${base}${location.search}`} replace />;
};

export default LegacyAppRunRedirect;
