/**
 * AppsPage — 应用中心（应用平台优先版 §3.2/§70）。
 *
 * 固定应用（kind ≠ chat）是代码开发的前后端产物：前端页面渲染器随代码
 * 发布，应用中心决定「是否启用」「是否公开」。
 *
 * 数据不再来自全局目录镜像：useApplicationPage 直连
 * `GET /v2/applications/page?kind=fixed`（执行报告 §24）—— LIMIT/cursor 在
 * SQL 层生效，搜索与分类作为 q / category_slug 传给后端，卡片开关更新后
 * 只做本地 patch（§31），不再整目录重新下载。
 *
 * 卡片点击 → `/app/:slug` 在 Shell 内打开（§32），返回时回到来源工作区。
 */
import React, { useEffect, useState } from 'react';
import { Button, Empty, Input, Spin, Switch, Tag, message } from 'antd';
import { ArrowRightOutlined, SearchOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useCatalogUiStore } from '@/stores/useCatalogUiStore';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { useAuthStore } from '@/stores/useAuthStore';
import { useApplicationPage } from '@/hooks/useApplicationPage';
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

/**
 * Cards fade in with a small stagger, CAPPED (执行报告 §22 note): `.app-card`
 * holds the first keyframe (opacity: 0) for the whole `animation-delay`, so an
 * uncapped `index * delay` would blank out the tail of a long list again.
 */
const STAGGER_MS = 60;
const STAGGER_MAX_STEPS = 8;
const cardDelay = (index: number): React.CSSProperties => ({
  animationDelay: `${Math.min(index, STAGGER_MAX_STEPS) * STAGGER_MS}ms`,
});

const AppsPage: React.FC = () => {
  const navigate = useNavigate();
  const isStaff = useAuthStore((state) => Boolean(state.user?.is_staff));
  // 分类筛选是页面间的共享 UI 状态（侧栏入口），放在 useCatalogUiStore；
  // 数据本身走服务端分页。
  const fixedCategory = useCatalogUiStore((state) => state.fixedCategory);
  const [searchQuery, setSearchQuery] = useState('');
  const [togglingId, setTogglingId] = useState<number | null>(null);

  const {
    items: apps,
    hasMore,
    loading,
    loadingMore,
    loadMore,
    patchItem,
  } = useApplicationPage({
    kind: 'fixed',
    scope: 'manage',
    includeUnbound: true,
    category: fixedCategory,
    query: searchQuery,
    limit: 24,
  });

  // Browsed rows seed the entity cache, so opening a card resolves from memory
  // instead of issuing a resolve request (执行报告 §10-B).
  const upsertEntities = useApplicationEntityStore((state) => state.upsertManyManage);
  useEffect(() => { upsertEntities(apps); }, [apps, upsertEntities]);

  /**
   * One mutation, THREE writes (二次复审 P1-3).
   *
   * A row this page has rendered is known in three places: the paged list,
   * the entity cache (a card click resolves `/app/:slug` from it without a
   * request), and the workspace bootstrap (首页 固定应用 + the category
   * rails). Writing only the first used to leave the other two stale: the
   * switch showed 停用 while the cached entity — the one a deep link opens —
   * still said enabled, and 首页 kept offering the app.
   */
  const applyApplicationMutation = (
    id: number,
    patch: Partial<V2Application>,
  ) => {
    patchItem(id, patch);
    useApplicationEntityStore.getState().patch(id, patch);

    if (patch.enabled === undefined && patch.is_public === undefined) return;
    // Visibility changed, so every bootstrap group's membership may have
    // changed too (a 停用 app leaves 常用应用; an enabled one may enter 最近
    // / 推荐 / a category count). The client cannot recompute a server-side
    // Top-8, so it marks the bootstrap stale and lets the next home visit
    // settle it (P1-4).
    useWorkspaceBootstrapStore.getState().invalidate();
    if (patch.enabled === false) {
      // Remove it NOW as well: 首页 must stop offering it in this session,
      // not after the user happens to reopen the home page.
      useWorkspaceBootstrapStore.getState().remove(id);
    }
  };

  const handleToggle = async (
    app: V2Application,
    patch: { enabled?: boolean; is_public?: boolean },
    hint: string,
  ) => {
    setTogglingId(app.id);
    try {
      await updateAgentApplication(app.id, patch);
      // Local patch only (执行报告 §31): the switch is the source of truth
      // the moment the server accepted it — re-downloading the whole catalog
      // for one boolean was the pre-pagination behaviour.
      applyApplicationMutation(app.id, patch);
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

      {loading && apps.length === 0 ? (
        <div className="flex items-center justify-center py-20">
          <Spin size="large" />
        </div>
      ) : apps.length === 0 ? (
        <div className="flex items-center justify-center py-20">
          <Empty description={<span className="text-text-sec">暂无应用</span>} />
        </div>
      ) : (
        <>
          <div className="app-grid">
            {apps.map((app, index) => (
              <div
                key={app.id}
                className={`app-card${app.enabled === false ? ' app-card--disabled' : ''}`}
                style={cardDelay(index)}
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
          <div className="apps-page__more">
            {hasMore ? (
              <Button onClick={() => void loadMore()} loading={loadingMore}>
                加载更多应用
              </Button>
            ) : (
              apps.length > 24 && (
                <span className="apps-page__count">已显示全部 {apps.length} 个</span>
              )
            )}
          </div>
        </>
      )}
    </div>
  );
};

export default AppsPage;
