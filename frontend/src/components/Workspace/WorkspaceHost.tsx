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
 */
import React, { useEffect, useMemo } from 'react';
import { Button, Empty, Spin } from 'antd';
import { useNavigate, useParams } from 'react-router-dom';
import { useApplicationCatalogStore } from '@/stores/useApplicationCatalogStore';
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
  const applications = useApplicationCatalogStore((state) => state.applications);
  const isLoading = useApplicationCatalogStore((state) => state.isLoading);
  const load = useApplicationCatalogStore((state) => state.load);
  const setActiveApplication = useWorkspaceStore((state) => state.setActiveApplication);
  const openApplication = useWorkspaceStore((state) => state.openApplication);

  useEffect(() => { void load(); }, [load]);

  const application = useMemo(
    () => (applicationSlug
      ? applications.find((app) => app.slug === applicationSlug)
      : undefined),
    [applications, applicationSlug]);

  useEffect(() => {
    if (application) openApplication(application.id);
    else if (!applicationSlug) setActiveApplication(null);
  }, [application, applicationSlug, openApplication, setActiveApplication]);

  if (!applicationSlug) return <HomeWorkspace />;

  if (!application) {
    if (isLoading) {
      return <div className="workspace-host__loading"><Spin size="large" /></div>;
    }
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
