/**
 * PageRenderer — fixed applications opened inside the shell (§32/§99).
 *
 * A fixed page (修改OA密码 / 条码查询 …) must open through
 * `AppShell → WorkspaceHost → PageRenderer` instead of a full page
 * navigation, so returning to the previous workspace restores it.
 *
 * The concrete UI is delegated to the renderer registry: the application's
 * `renderer_key` selects the runner, exactly like the console runtime page.
 */
import React, { useEffect, useState } from 'react';
import { Button, Empty, Spin } from 'antd';
import { ArrowLeftOutlined, StarFilled, StarOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import ApplicationRuntime from '@/components/Applications/ApplicationRuntime';
import { useAppStore } from '@/stores/useAppStore';
import { useApplicationCatalogStore } from '@/stores/useApplicationCatalogStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import type { AppItem, ApplicationRuntime as ApplicationRuntimeData } from '@/types';
import type { V2Application } from '@/services/runApi';
import ApplicationSwitcher from './ApplicationSwitcher';

interface Props {
  application: V2Application;
  /** Label of the workspace kind shown in the header (页面 / 工作流). */
  kindLabel?: string;
}

const PageRenderer: React.FC<Props> = ({ application, kindLabel = '应用' }) => {
  const navigate = useNavigate();
  const loadApp = useAppStore((state) => state.loadApp);
  const toggleFavorite = useApplicationCatalogStore((state) => state.toggleFavorite);
  const openApplication = useWorkspaceStore((state) => state.openApplication);
  const [app, setApp] = useState<AppItem | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => { openApplication(application.id); }, [application.id, openApplication]);

  useEffect(() => {
    let active = true;
    setLoading(true);
    loadApp(application.slug)
      .then((loaded) => { if (active) setApp(loaded); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [application.slug, loadApp]);

  return (
    <div className="workspace-host">
      <header className="chat-renderer__head">
        <Button
          type="text"
          size="small"
          icon={<ArrowLeftOutlined />}
          onClick={() => navigate('/')}
        >
          返回工作台
        </Button>
        <span className="application-switcher__icon">{application.icon || '✦'}</span>
        <strong>{application.name}</strong>
        <span className="home-shortcut__desc">
          {kindLabel}
          {application.renderer_key ? ` · ${application.renderer_key}` : ''}
        </span>
        <span className="chat-renderer__head-spacer" />
        <Button
          type="text"
          aria-label={application.is_favorite ? '取消收藏' : '收藏'}
          icon={application.is_favorite ? <StarFilled /> : <StarOutlined />}
          onClick={() => void toggleFavorite(application.id)}
        />
      </header>
      <div className="chat-renderer__body">
        {loading ? (
          <div className="workspace-host__loading"><Spin size="large" /></div>
        ) : app?.runtime ? (
          <div className="workspace-host__frame">
            <ApplicationRuntime application={app.runtime as ApplicationRuntimeData} />
          </div>
        ) : (
          <div className="workspace-host__missing">
            <Empty
              image={Empty.PRESENTED_IMAGE_SIMPLE}
              description="该应用暂无可运行配置"
            />
          </div>
        )}
      </div>
    </div>
  );
};

export default PageRenderer;
