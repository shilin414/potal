/**
 * WorkspaceHost — the frontend's core component (§17/§18).
 *
 * It resolves the route's application and decides which workspace renders:
 *
 *   HomeWorkspace · ChatRenderer · PageRenderer · WorkflowRenderer
 *   (FormRenderer / DashboardRenderer / TaskRenderer arrive with the
 *    renderer registry, §100)
 *
 * `Chat` is only one of them. Everything here is provider-agnostic: the
 * backend already reduced a binding to capabilities + a renderer key.
 *
 * Slug resolution (执行报告 §12, P1-2) is EXACT and one row wide: the route's
 * slug is looked up in the application entity cache and, on a miss, resolved
 * with `GET /applications/resolve?slug=`. Opening `/chat/sales` used to
 * download the entire catalog and run `Array.find()` over it.
 *
 * 404 and "the server is down" are DIFFERENT states (二次复审 P1-6). The
 * entity store only swallows a real 404 now, so this component can tell
 * "找不到应用" apart from "应用加载失败" and offer a retry — instead of
 * reporting a network outage as a missing application.
 */
import React, { useCallback, useEffect, useState } from 'react';
import { Button, Empty, Spin } from 'antd';
import { useNavigate, useParams } from 'react-router-dom';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
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
  const bySlug = useApplicationEntityStore((state) => state.bySlug);
  const ensureBySlug = useApplicationEntityStore((state) => state.ensureBySlug);
  const setActiveApplication = useWorkspaceStore((state) => state.setActiveApplication);
  const openApplication = useWorkspaceStore((state) => state.openApplication);
  const [resolving, setResolving] = useState(false);
  /** Distinguishes "no such application" from "could not ask" (P1-6). */
  const [resolveFailed, setResolveFailed] = useState(false);

  const application = applicationSlug ? bySlug[applicationSlug] : undefined;

  const resolve = useCallback(async (slug: string) => {
    setResolving(true);
    setResolveFailed(false);
    try {
      // Every ENTRY into a consumer route revalidates (四次复审 P1-R1): a
      // cached row that predates an admin's disable / provider kill switch
      // must not be trusted just because it is still in memory.
      await ensureBySlug(slug, { maxAgeMs: 0 });
    } catch {
      // A 500 / timeout / offline backend. Deliberately NOT folded into
      // "not found": the slug is probably fine, so the user gets a retry
      // instead of a dead end (and their URL is left alone).
      setResolveFailed(true);
    } finally {
      setResolving(false);
    }
  }, [ensureBySlug]);

  useEffect(() => {
    if (!applicationSlug) {
      setResolving(false);
      setResolveFailed(false);
      setActiveApplication(null);
      return undefined;
    }
    let active = true;
    setResolving(true);
    // Cache-first: a slug already visited resolves synchronously, so the
    // workspace does not flash a spinner on a route change inside the shell.
    void resolve(applicationSlug).finally(() => {
      if (!active) setResolveFailed(false);
    });
    return () => { active = false; };
  }, [applicationSlug, resolve, setActiveApplication]);

  useEffect(() => {
    if (application) openApplication(application.id);
  }, [application, openApplication]);

  if (!applicationSlug) return <HomeWorkspace />;

  if (!application) {
    if (resolving) {
      return <div className="workspace-host__loading"><Spin size="large" /></div>;
    }
    if (resolveFailed) {
      return (
        <div className="workspace-host__missing">
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description="应用加载失败，请检查网络后重试"
          >
            <Button type="primary" onClick={() => void resolve(applicationSlug)}>
              重新加载
            </Button>
            <Button onClick={() => navigate('/')}>返回工作台</Button>
          </Empty>
        </div>
      );
    }
    // A missing application and a FORBIDDEN one look the same here, which is
    // the point: /applications/resolve answers 404 for both, so the shell
    // cannot be used to probe which slugs exist.
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

  // A chat application is always a chat workspace, whichever route shape
  // brought the user here; other kinds follow the renderer registry (§100).
  if (kind === 'chat' || application.kind === 'chat'
    || application.renderer_key === 'chat') {
    return <ChatRenderer key={application.id} application={application} />;
  }
  if (kind === 'workflow') return <WorkflowRenderer application={application} />;
  return <PageRenderer application={application} />;
};

export default WorkspaceHost;
