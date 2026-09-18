/**
 * Resolve the route's application before mounting any consumer renderer.
 *
 * The entity cache can accelerate shared reads, but it cannot admit a route:
 * a management page may have cached a disabled/private/unbound application.
 * Only the result of this entry's forced consume `/resolve` may mount the
 * chat/page/workflow surface.
 */
import React, { useEffect, useState } from 'react';
import { Button, Empty, Spin } from 'antd';
import { useNavigate, useParams } from 'react-router-dom';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import type { V2Application } from '@/services/runApi';
import ChatRenderer from './ChatRenderer';
import HomeWorkspace from './HomeWorkspace';
import PageRenderer from './PageRenderer';
import WorkflowRenderer from './WorkflowRenderer';

export type WorkspaceKind = 'home' | 'chat' | 'page' | 'workflow';

interface Props {
  kind?: WorkspaceKind;
}

const WorkspaceHost: React.FC<Props> = ({ kind = 'home' }) => {
  const navigate = useNavigate();
  const { applicationSlug } = useParams<{ applicationSlug?: string }>();
  const ensureBySlug = useApplicationEntityStore((state) => state.ensureBySlug);
  const setActiveApplication = useWorkspaceStore((state) => state.setActiveApplication);
  const openApplication = useWorkspaceStore((state) => state.openApplication);
  // undefined = resolve pending, null = authoritative 404, object = admitted.
  const [resolvedApplication, setResolvedApplication] = useState<
    V2Application | null | undefined
  >(undefined);
  const [resolvedSlug, setResolvedSlug] = useState<string | null>(null);
  const [resolveFailed, setResolveFailed] = useState(false);
  const [retryNonce, setRetryNonce] = useState(0);

  useEffect(() => {
    if (!applicationSlug) {
      setResolvedSlug(null);
      setResolvedApplication(null);
      setResolveFailed(false);
      setActiveApplication(null);
      return undefined;
    }

    let active = true;
    setActiveApplication(null);
    setResolvedSlug(applicationSlug);
    setResolvedApplication(undefined);
    setResolveFailed(false);
    void ensureBySlug(applicationSlug, { maxAgeMs: 0, bypassBackoff: retryNonce > 0 })
      .then((application) => {
        if (!active) return;
        setResolvedApplication(application ?? null);
      })
      .catch(() => {
        if (!active) return;
        setResolveFailed(true);
      });
    return () => { active = false; };
  }, [applicationSlug, ensureBySlug, retryNonce, setActiveApplication]);

  useEffect(() => {
    if (resolvedApplication) openApplication(resolvedApplication.id);
  }, [resolvedApplication, openApplication]);

  if (!applicationSlug) return <HomeWorkspace />;

  // Effects run after render. Associate the admission result with its lookup
  // key so a route change cannot render the previous slug for one frame.
  if (resolvedSlug !== applicationSlug) {
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
          <Button onClick={() => navigate('/')}>返回工作台</Button>
        </Empty>
      </div>
    );
  }

  if (resolvedApplication === undefined) {
    return <div className="workspace-host__loading"><Spin size="large" /></div>;
  }

  if (resolvedApplication === null) {
    return (
      <div className="workspace-host__missing">
        <Empty
          image={Empty.PRESENTED_IMAGE_SIMPLE}
          description={`找不到应用：${applicationSlug}`}
        >
          <Button type="primary" onClick={() => navigate('/')}>返回工作台</Button>
        </Empty>
      </div>
    );
  }

  if (kind === 'chat' || resolvedApplication.kind === 'chat'
    || resolvedApplication.renderer_key === 'chat') {
    return (
      <ChatRenderer
        key={resolvedApplication.id}
        application={resolvedApplication}
      />
    );
  }
  if (kind === 'workflow') {
    return <WorkflowRenderer application={resolvedApplication} />;
  }
  return <PageRenderer application={resolvedApplication} />;
};

export default WorkspaceHost;
