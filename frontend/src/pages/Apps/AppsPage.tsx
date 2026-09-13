/**
 * AppsPage — 应用中心（应用平台优先版 §3.2/§70）。
 *
 * 固定应用（kind ≠ chat）是代码开发的前后端产物：前端页面渲染器随代码
 * 发布，应用中心决定「是否启用」「是否公开」。数据直接来自 v2 应用目录
 * （AppShell 已加载，scope=manage：普通用户只见公开+启用的应用，管理员
 * 额外见到私有/停用的应用以便管理）。
 *
 * 卡片点击 → `/app/:slug` 在 Shell 内打开（§32），返回时回到来源工作区。
 */
import React, { useEffect, useMemo, useState } from 'react';
import { Empty, Input, Spin, Switch, Tag, message } from 'antd';
import { ArrowRightOutlined, SearchOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useApplicationCatalogStore } from '@/stores/useApplicationCatalogStore';
import { useAuthStore } from '@/stores/useAuthStore';
import { updateAgentApplication } from '@/services/runApi';
import type { V2Application } from '@/services/runApi';
import './AppsPage.css';

const { Search } = Input;

const KIND_LABELS: Record<string, string> = {
  page: '页面',
  form: '表单',
  dashboard: '看板',
  custom: '应用',
};

const AppsPage: React.FC = () => {
  const navigate = useNavigate();
  const isStaff = useAuthStore((state) => Boolean(state.user?.is_staff));
  const applications = useApplicationCatalogStore((state) => state.applications);
  const isLoading = useApplicationCatalogStore((state) => state.isLoading);
  const load = useApplicationCatalogStore((state) => state.load);
  const fixedCategory = useApplicationCatalogStore((state) => state.fixedCategory);
  const setFixedCategory = useApplicationCatalogStore((state) => state.setFixedCategory);
  const [searchQuery, setSearchQuery] = useState('');
  const [togglingId, setTogglingId] = useState<number | null>(null);

  useEffect(() => { void load(); }, [load]);

  // 固定应用 = 目录里所有非 chat 的 Application；chat 智能体属于智能体市场。
  const fixedApps = useMemo(
    () => applications.filter((app) => app.kind !== 'chat'),
    [applications]);

  const visibleApps = useMemo(() => {
    const keyword = searchQuery.trim().toLowerCase();
    return fixedApps.filter((app) => {
      if (fixedCategory && app.category_slug !== fixedCategory) return false;
      if (!keyword) return true;
      return app.name.toLowerCase().includes(keyword)
        || (app.description || '').toLowerCase().includes(keyword);
    });
  }, [fixedApps, fixedCategory, searchQuery]);

  const handleToggle = async (
    app: V2Application,
    patch: { enabled?: boolean; is_public?: boolean },
    hint: string,
  ) => {
    setTogglingId(app.id);
    try {
      await updateAgentApplication(app.id, patch);
      await load(true);
      message.success(`${app.name} ${hint}`);
    } catch (error: any) {
      message.error(error?.response?.data?.detail || '操作失败，请重试');
    } finally {
      setTogglingId(null);
    }
  };

  return (
    <div className="apps-page animate-fade-in">
      <div className="page-header">
        <h1 className="page-title">应用中心</h1>
        <p className="page-subtitle">企业固定业务应用，点开即用；页面随代码发布，由应用中心决定是否启用</p>
      </div>

      <div className="page-toolbar">
        <Search
          placeholder="搜索应用..."
          prefix={<SearchOutlined className="text-text-dim" />}
          value={searchQuery}
          onChange={(e) => setSearchQuery(e.target.value)}
          allowClear
          className="max-w-xs"
        />
      </div>

      {isLoading && fixedApps.length === 0 ? (
        <div className="flex items-center justify-center py-20">
          <Spin size="large" />
        </div>
      ) : visibleApps.length === 0 ? (
        <div className="flex items-center justify-center py-20">
          <Empty description={<span className="text-text-sec">暂无应用</span>} />
        </div>
      ) : (
        <div className="app-grid">
          {visibleApps.map((app, index) => (
            <div
              key={app.id}
              className={`app-card${app.enabled === false ? ' app-card--disabled' : ''}`}
              style={{ animationDelay: `${index * 60}ms` }}
              role="button"
              tabIndex={0}
              onClick={() => navigate(`/app/${app.slug}`)}
              onKeyDown={(event) => {
                if (event.key === 'Enter' || event.key === ' ') {
                  event.preventDefault();
                  navigate(`/app/${app.slug}`);
                }
              }}
            >
              <div
                className="app-card-thumb"
                style={
                  app.color
                    ? { background: `linear-gradient(135deg, ${app.color}, color-mix(in srgb, ${app.color} 40%, #000))` }
                    : undefined
                }
              >
                <span className="app-card-emoji">{app.icon || '🧩'}</span>
              </div>
              <div className="app-card-body">
                <div className="app-card-name">
                  {app.name}
                  {app.enabled === false && (
                    <Tag className="app-card-state app-card-state--off" bordered={false}>已停用</Tag>
                  )}
                  {isStaff && !app.is_public && (
                    <Tag className="app-card-state" bordered={false}>仅自己可见</Tag>
                  )}
                </div>
                <div className="app-card-desc">{app.description}</div>
                <div className="app-card-footer">
                  <div className="app-card-tags">
                    {app.category_name && (
                      <span className="app-card-cat">{app.category_name}</span>
                    )}
                    <span className="app-card-tag">{KIND_LABELS[app.kind] || app.kind}</span>
                  </div>
                  {isStaff ? (
                    <div className="app-card-admin" onClick={(e) => e.stopPropagation()}>
                      <label className="app-card-switch">
                        <Switch
                          size="small"
                          checked={app.enabled !== false}
                          loading={togglingId === app.id}
                          checkedChildren="启用"
                          unCheckedChildren="停用"
                          onChange={(checked) => void handleToggle(
                            app, { enabled: checked }, checked ? '已启用' : '已停用')}
                        />
                      </label>
                      <label className="app-card-switch">
                        <Switch
                          size="small"
                          checked={Boolean(app.is_public)}
                          loading={togglingId === app.id}
                          checkedChildren="公开"
                          unCheckedChildren="私有"
                          onChange={(checked) => void handleToggle(
                            app, { is_public: checked }, checked ? '已公开' : '已设为仅自己可见')}
                        />
                      </label>
                    </div>
                  ) : (
                    <span className="app-card-open">
                      打开 <ArrowRightOutlined />
                    </span>
                  )}
                </div>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
};

export default AppsPage;
