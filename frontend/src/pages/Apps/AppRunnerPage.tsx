import React, { useEffect, useState } from 'react';
import { Button, Result, Spin } from 'antd';
import { ArrowLeftOutlined } from '@ant-design/icons';
import { useParams, useNavigate } from 'react-router-dom';
import { useAppStore } from '@/stores/useAppStore';
import { useAppWorkspace } from '@/hooks/useAppWorkspace';
import type { AppItem } from '@/types';
import AssetPanel from '@/components/Workspace/AssetPanel';
import './AppRunnerPage.css';

// AppRunnerPage — the launched app view at /apps/:id/run.
const AppRunnerPage: React.FC = () => {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { loadApp } = useAppStore();

  const [app, setApp] = useState<AppItem | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!id) {
      setLoading(false);
      setApp(null);
      return;
    }
    setLoading(true);
    loadApp(id)
      .then(setApp)
      .finally(() => setLoading(false));
  }, [id, loadApp]);

  // Create / load a workspace for this app run.
  const { assets } = useAppWorkspace(app);

  if (loading) {
    return (
      <div className="app-runner-page">
        <div className="flex items-center justify-center py-20">
          <Spin size="large" />
        </div>
      </div>
    );
  }

  if (!app) {
    return (
      <div className="app-runner-page">
        <Result
          status="404"
          title="应用不存在"
          subTitle="该应用可能已下线或链接有误。"
          extra={
            <Button type="primary" onClick={() => navigate('/apps')}>
              返回应用中心
            </Button>
          }
        />
      </div>
    );
  }

  return (
    <div className="app-runner-page animate-fade-in">
      <div className="app-runner-topbar">
        <Button
          type="text"
          icon={<ArrowLeftOutlined />}
          onClick={() => navigate('/apps')}
          className="app-runner-back"
        >
          返回应用中心
        </Button>
        <div className="app-runner-app">
          <span className="app-runner-emoji">{app.icon}</span>
          <span className="app-runner-name">{app.name}</span>
        </div>
      </div>

      <div className="app-runner-main">
        <div className="app-runner-stage">
          <div
            className="app-runner-canvas"
            style={
              app.color
                ? { background: `linear-gradient(135deg, ${app.color}, color-mix(in srgb, ${app.color} 40%, #000))` }
                : undefined
            }
          >
            <div className="app-runner-canvas-emoji">{app.icon}</div>
            <div className="app-runner-canvas-title">{app.name}</div>
            <div className="app-runner-canvas-hint">
              应用已启动，真实工作区内容将按应用类型在此接入。
            </div>
          </div>
        </div>

        <AssetPanel
          assets={assets}
          hint="生成的文件会自动保存到这里"
        />
      </div>
    </div>
  );
};

export default AppRunnerPage;
