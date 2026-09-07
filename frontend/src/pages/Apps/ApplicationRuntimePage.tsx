import { useEffect, useState } from 'react';
import { Button, Result, Spin } from 'antd';
import { ArrowLeftOutlined } from '@ant-design/icons';
import { useNavigate, useParams } from 'react-router-dom';
import ApplicationRuntime from '@/components/Applications/ApplicationRuntime';
import { useAppWorkspace } from '@/hooks/useAppWorkspace';
import { useAppStore } from '@/stores/useAppStore';
import type { AppItem } from '@/types';
import './ApplicationRuntimePage.css';

const ApplicationRuntimePage = () => {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const loadApp = useAppStore((state) => state.loadApp);
  const [app, setApp] = useState<AppItem | null>(null);
  const [loading, setLoading] = useState(true);
  const workspace = useAppWorkspace(app);

  useEffect(() => {
    if (!id) return;
    setLoading(true);
    loadApp(id).then(setApp).finally(() => setLoading(false));
  }, [id, loadApp]);

  if (loading || (app?.runtime && !workspace.isWorkspaceReady && !workspace.workspaceError)) {
    return <div className="application-runtime-loading"><Spin size="large" /></div>;
  }
  if (workspace.workspaceError) return (
    <Result status="error" title={workspace.workspaceError}
      extra={<Button onClick={() => navigate('/apps')}>返回应用中心</Button>} />
  );
  if (!app?.runtime) return (
    <Result status="404" title="应用没有可运行配置"
      extra={<Button onClick={() => navigate('/apps')}>返回应用中心</Button>} />
  );

  return (
    <div className="application-runtime-page">
      <header className="application-runtime-header">
        <Button type="text" icon={<ArrowLeftOutlined />} onClick={() => navigate('/apps')}>
          应用中心
        </Button>
        <span className="application-runtime-icon">{app.icon}</span>
        <strong>{app.name}</strong>
      </header>
      <main className="application-runtime-main">
        <ApplicationRuntime key={workspace.projectId ?? 'new'} application={app.runtime}
          projectId={workspace.projectId ?? undefined} />
      </main>
    </div>
  );
};

export default ApplicationRuntimePage;
