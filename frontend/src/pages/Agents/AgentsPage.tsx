/**
 * AgentsPage — 智能体市场.
 *
 * Shows the two kinds of agent Studio can host, in one grid:
 *
 *  - 运行时智能体 (Aily 自定义智能体 ...): Applications with a runtime binding —
 *    these are the ones the workspace can actually run, and the ones the
 *    「新建智能体」 form authors. Actions: 打开 / 编辑 / 改头像 / 设为主智能体 / 删除.
 *  - 本地创作智能体: legacy Agent rows (system prompt + skills) driving the old
 *    GraphFlow workspace path. Kept visible so nothing regresses.
 *
 * The provider label on each card comes from `GET /api/v2/runtimes`, so no
 * provider name is hard-coded in the frontend.
 */
import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  Avatar, Button, Empty, Input, Popconfirm, Segmented, Spin, Tag, Tooltip, message,
} from 'antd';
import {
  DeleteOutlined, EditOutlined, PictureOutlined, PlusOutlined,
  SearchOutlined, StarFilled, StarOutlined,
} from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useAgentStore } from '@/stores/useAgentStore';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { useAuthStore } from '@/stores/useAuthStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import { useApplicationPage } from '@/hooks/useApplicationPage';
import AgentDetailModal from '@/components/Agents/AgentDetailModal';
import AgentEditorModal, { type AgentEditorMode } from '@/components/Agents/AgentEditorModal';
import AgentAvatarModal from '@/components/Agents/AgentAvatarModal';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import { api } from '@/services/api';
import {
  deleteAgentApplication,
  fetchAgentRuntimes,
  setDefaultAgent,
  type AgentRuntimeDescriptor,
  type ManagedAgent,
  type V2Application,
} from '@/services/runApi';
import './AgentsPage.css';

const { Search } = Input;

const AGENT_ICONS = ['🎬', '✍️', '🎙️', '✂️', '🎨', '🎵', '💡', '🔧'];

/**
 * The server page size — the ONLY pagination budget (执行报告 §22).
 *
 * Before the paged endpoint existed this constant sliced a whole-catalog
 * array in the browser (`visibleCount`/`slice`): 加载更多 rendered fewer DOM
 * nodes but still downloaded every application. Now the grid shows exactly
 * what `GET /v2/applications/page?limit=24` returned, and 加载更多 fetches
 * the next server page.
 */
const PAGE_SIZE = 24;

/**
 * Cards fade in with a small stagger — but only for the first few.
 *
 * `.agent-card` is `animation: fadeIn .4s ... both`, and `both` holds the
 * FIRST keyframe (opacity: 0) for the whole `animation-delay`. An uncapped
 * `index * delay` therefore makes a large catalog look EMPTY. The cap keeps
 * the entrance animation independent of list position.
 */
const STAGGER_MS = 40;
const STAGGER_MAX_STEPS = 8;
const cardDelay = (index: number): React.CSSProperties => ({
  animationDelay: `${Math.min(index, STAGGER_MAX_STEPS) * STAGGER_MS}ms`,
});

type SourceFilter = 'all' | 'runtime' | 'local';

const AgentsPage: React.FC = () => {
  const navigate = useNavigate();

  // Local (Agent rows) — still filtered server-side by the category rail.
  const {
    agents,
    isLoading,
    selectedCategory,
    searchQuery,
    loadAgents,
    setSearchQuery,
  } = useAgentStore();

  // Runtime agents (Applications + bindings) — SERVER-paged (执行报告 §22):
  // search and the category rail go to the backend as q / category_slug, and
  // 加载更多 fetches the next cursor page instead of revealing a browser-side
  // slice of a fully-downloaded catalog.
  const {
    items: appAgents,
    hasMore: appsHasMore,
    loading: loadingApps,
    loadingMore: loadingMoreApps,
    loadMore: loadMoreApps,
    refresh: refreshAppAgents,
    patchItem: patchAppAgent,
  } = useApplicationPage({
    kind: 'chat',
    scope: 'manage',
    includeUnbound: true,
    category: selectedCategory,
    query: searchQuery,
    limit: PAGE_SIZE,
  });

  const [runtimes, setRuntimes] = useState<AgentRuntimeDescriptor[]>([]);
  const [source, setSource] = useState<SourceFilter>('all');

  // Rows the user has SEEN are entities the workspace may be asked to open, so
  // every fetched page seeds the entity cache: clicking a card then costs no
  // resolve request at all (执行报告 §10-B).
  const upsertEntities = useApplicationEntityStore((state) => state.upsertMany);
  useEffect(() => { upsertEntities(appAgents); }, [appAgents, upsertEntities]);

  const [selectedAgentId, setSelectedAgentId] = useState<number | null>(null);
  const [modalOpen, setModalOpen] = useState(false);
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingAgentId, setEditingAgentId] = useState<number | null>(null);
  const [editingMode, setEditingMode] = useState<AgentEditorMode>('runtime');
  const [avatarAgent, setAvatarAgent] = useState<ManagedAgent | null>(null);
  const [deletingAgentId, setDeletingAgentId] = useState<number | null>(null);
  const [busyAppId, setBusyAppId] = useState<number | null>(null);

  const openApplication = useWorkspaceStore((state) => state.openApplication);
  // Local patches replace the old `reloadCatalog(true)` (执行报告 §15, P1-3):
  // every mutation below knows exactly which objects changed — the current
  // page row, the entity cache entry and the bootstrap groups — so none of
  // them needs the whole catalog back. The page row is the authority for the
  // grid; the other two keep the shell (switcher, home shortcuts, deep links)
  // consistent with it.
  const upsertEntity = useApplicationEntityStore((state) => state.upsert);
  const removeEntity = useApplicationEntityStore((state) => state.remove);
  const patchBootstrap = useWorkspaceBootstrapStore((state) => state.patch);
  const removeFromBootstrap = useWorkspaceBootstrapStore((state) => state.remove);
  const reloadBootstrap = useWorkspaceBootstrapStore((state) => state.load);
  // 智能体接入是管理员能力：普通用户只消费市场。
  const isStaff = useAuthStore((state) => Boolean(state.user?.is_staff));
  const isFirstRun = useRef(true);

  useEffect(() => {
    fetchAgentRuntimes().then(setRuntimes).catch(() => setRuntimes([]));
  }, []);

  useEffect(() => {
    // First load fires immediately; later category/search changes are debounced.
    // The isFirstRun guard avoids a second fetch on the initial mount.
    if (isFirstRun.current) {
      isFirstRun.current = false;
      loadAgents(selectedCategory || undefined);
      return;
    }
    const timer = setTimeout(() => {
      loadAgents(selectedCategory || undefined);
    }, 300);
    return () => clearTimeout(timer);
  }, [selectedCategory, searchQuery, loadAgents]);

  /**
   * A MUTATION path: refresh the two lists this page owns (the paged runtime
   * grid and the legacy local agents) without touching the workspace
   * bootstrap — the caller decides whether the bootstrap needs anything.
   */
  const refreshAll = useCallback(async () => {
    await Promise.all([
      loadAgents(useAgentStore.getState().selectedCategory || undefined),
      refreshAppAgents(),
    ]);
  }, [loadAgents, refreshAppAgents]);

  /** Runtime label per card, read from the runtime catalog (never hard-coded). */
  const labelFor = useCallback((agent: V2Application) => {
    if (!agent.provider_key) return '未绑定运行时';
    return runtimes.find((item) => (
      item.key === `${agent.provider_key}:${agent.runtime_type}`
    ))?.label || agent.provider_key;
  }, [runtimes]);

  const showRuntime = source !== 'local';
  const showLocal = source !== 'runtime';
  // The runtime section is server-paged: `appAgents` is exactly what the API
  // returned for the current filters. The LOCAL (legacy GraphFlow) section is
  // the Django `/agents` listing — a small, separately server-searched set —
  // so it renders whole; only the runtime side carries 加载更多.
  const pagedRuntime = showRuntime ? appAgents : [];
  const pagedLocal = showLocal ? agents : [];
  const totalLoaded = (showRuntime ? appAgents.length : 0)
    + (showLocal ? agents.length : 0);

  const isBusy = isLoading || loadingApps;
  const isEmpty = (!showRuntime || appAgents.length === 0)
    && (!showLocal || agents.length === 0);

  const openEditor = (agentId: number | null, mode: AgentEditorMode) => {
    setEditingAgentId(agentId);
    setEditingMode(mode);
    setEditorOpen(true);
  };

  const openRuntimeAgent = async (agent: V2Application) => {
    if (!agent.is_bound) {
      message.warning('该智能体还没有可用的运行时绑定，无法对话；请先编辑补全。');
      return;
    }
    // The workspace resolves the slug from the ENTITY cache (`/applications/
    // resolve`), so "open" seeds that cache instead of reloading the catalog
    // — opening an agent costs no list request at all (执行报告 §15).
    upsertEntity(agent);
    openApplication(agent.id);
    navigate(`/chat/${agent.slug}`);
  };

  const handleSetDefault = async (agent: V2Application, next: boolean) => {
    setBusyAppId(agent.id);
    try {
      const updated = await setDefaultAgent(agent.id, next);
      message.success(next
        ? `「${agent.name}」已设为工作台默认智能体`
        : `「${agent.name}」已取消默认智能体`);
      // The response IS the new state: patch the page row, the entity cache
      // and the bootstrap's default agent. Nothing else changed locally, so no
      // list is refetched (执行报告 §15 "设置默认智能体").
      const patch: Partial<V2Application> = {
        is_default_agent: updated.is_default_agent,
        ...(updated.avatar_url ? { avatar_url: updated.avatar_url } : {}),
      };
      patchAppAgent(agent.id, patch);
      upsertEntity({ ...agent, ...patch });
      patchBootstrap(agent.id, { is_default_agent: updated.is_default_agent });
      // Demoting the PREVIOUS default is a server-side side effect we cannot
      // see locally, so one bootstrap refresh (tens of rows, never the
      // catalog) settles the flag where it also lives.
      await reloadBootstrap(true);
    } catch (error: any) {
      message.error(error?.response?.data?.detail || '设置默认智能体失败');
    } finally {
      setBusyAppId(null);
    }
  };

  const handleDeleteRuntimeAgent = async (agent: V2Application) => {
    setBusyAppId(agent.id);
    try {
      await deleteAgentApplication(agent.id);
      message.success('智能体已删除');
      // Remove the row everywhere it is known instead of re-downloading any
      // list: page, entity cache, bootstrap groups (执行报告 §15 "删除").
      removeEntity(agent.id);
      removeFromBootstrap(agent.id);
      await refreshAll();
    } catch (error: any) {
      message.error(error?.response?.data?.detail || '删除智能体失败');
    } finally {
      setBusyAppId(null);
    }
  };

  const handleDeleteLocalAgent = async (agentId: number) => {
    setDeletingAgentId(agentId);
    try {
      await api.delete(`/agents/${agentId}/`);
      message.success('智能体已删除');
      await refreshAll();
    } catch (error: any) {
      message.error(error?.response?.data?.detail || '删除智能体失败');
    } finally {
      setDeletingAgentId(null);
    }
  };

  const runtimeCard = (agent: V2Application, index: number) => (
    <div
      key={`app-${agent.id}`}
      className="agent-card"
      style={cardDelay(index)}
      onClick={() => void openRuntimeAgent(agent)}
    >
      {agent.can_manage && (
        <div className="agent-card-actions" onClick={(event) => event.stopPropagation()}>
          <Tooltip title="编辑">
            <Button
              type="text"
              size="small"
              icon={<EditOutlined />}
              aria-label={`编辑 ${agent.name}`}
              onClick={() => openEditor(agent.id, 'runtime')}
            />
          </Tooltip>
          <Tooltip title="改头像">
            <Button
              type="text"
              size="small"
              icon={<PictureOutlined />}
              aria-label={`修改 ${agent.name} 的头像`}
              // The avatar modal predates the paged endpoint and speaks the
              // authoring payload; the paged item carries the same fields.
              onClick={() => setAvatarAgent(agent as ManagedAgent)}
            />
          </Tooltip>
          <Tooltip title={agent.is_default_agent ? '取消默认智能体' : '设为工作台默认智能体'}>
            <Button
              type="text"
              size="small"
              icon={agent.is_default_agent ? <StarFilled /> : <StarOutlined />}
              loading={busyAppId === agent.id}
              aria-label={`设置 ${agent.name} 为默认智能体`}
              onClick={() => void handleSetDefault(agent, !agent.is_default_agent)}
            />
          </Tooltip>
          <Popconfirm
            title="删除智能体"
            description={`确定删除“${agent.name}”吗？此操作不可撤销。`}
            okText="删除"
            cancelText="取消"
            okButtonProps={{ danger: true }}
            onConfirm={() => void handleDeleteRuntimeAgent(agent)}
          >
            <Tooltip title="删除">
              <Button
                type="text"
                danger
                size="small"
                icon={<DeleteOutlined />}
                loading={busyAppId === agent.id}
                aria-label={`删除 ${agent.name}`}
              />
            </Tooltip>
          </Popconfirm>
        </div>
      )}
      {/* One avatar component everywhere (执行报告 §11): same lazy loading,
          fallback, shape and error handling as the switcher / home / chat. */}
      <AgentAvatar
        application={agent}
        size={48}
        shape="square"
        tint={agent.color}
        className="agent-card-avatar-slot"
      />
      <div className="agent-card-name">
        {agent.name}
        {agent.is_default_agent && <span className="agent-card-main">主</span>}
      </div>
      <div className="agent-card-desc">{agent.description}</div>
      <div className="agent-card-footer">
        <span className="agent-card-tags">
          <Tag className="agent-tag">{labelFor(agent)}</Tag>
          {agent.category_name && <Tag className="agent-tag">{agent.category_name}</Tag>}
          {!agent.is_bound && <Tag color="warning">未绑定</Tag>}
          {!agent.is_public && <Tag className="agent-tag">私有</Tag>}
        </span>
        <span className="agent-card-meta">
          ⚡ {(agent.usage_count || 0).toLocaleString()} 次使用
        </span>
      </div>
    </div>
  );

  const localCard = (agent: (typeof agents)[number], index: number) => (
    <div
      key={`agent-${agent.id}`}
      className="agent-card"
      style={cardDelay(index)}
      onClick={() => { setSelectedAgentId(agent.id); setModalOpen(true); }}
    >
      {(agent.can_edit || agent.can_delete) && (
        <div className="agent-card-actions" onClick={(event) => event.stopPropagation()}>
          {agent.can_edit && (
            <Tooltip title="编辑">
              <Button
                type="text"
                size="small"
                icon={<EditOutlined />}
                aria-label={`编辑 ${agent.name}`}
                onClick={() => openEditor(agent.id, 'local')}
              />
            </Tooltip>
          )}
          {agent.can_delete && (
            <Popconfirm
              title="删除智能体"
              description={`确定删除“${agent.name}”吗？此操作不可撤销。`}
              okText="删除"
              cancelText="取消"
              okButtonProps={{ danger: true }}
              onConfirm={() => void handleDeleteLocalAgent(agent.id)}
            >
              <Tooltip title="删除">
                <Button
                  type="text"
                  danger
                  size="small"
                  icon={<DeleteOutlined />}
                  loading={deletingAgentId === agent.id}
                  aria-label={`删除 ${agent.name}`}
                />
              </Tooltip>
            </Popconfirm>
          )}
        </div>
      )}
      <div className={`agent-card-icon icon-gradient-${(index % 6) + 1}`}>
        {agent.icon || AGENT_ICONS[index % AGENT_ICONS.length]}
      </div>
      <div className="agent-card-name">{agent.name}</div>
      <div className="agent-card-desc">{agent.description}</div>
      <div className="agent-card-footer">
        <span className="agent-card-tags">
          <Tag className="agent-tag">本地创作智能体</Tag>
          {agent.category_name && <Tag className="agent-tag">{agent.category_name}</Tag>}
        </span>
        <span className="agent-card-meta">
          ⚡ {((agent as any).usage_count || 0).toLocaleString()} 次使用
        </span>
      </div>
    </div>
  );

  return (
    <div className="agents-page animate-fade-in">
      <div className="page-header">
        <h1 className="page-title">智能体市场</h1>
        <p className="page-subtitle">
          新建 Aily 自定义智能体、设置工作台默认智能体，或管理已有的本地创作智能体
        </p>
      </div>

      <div className="page-toolbar">
        <div className="page-toolbar-left">
          {isStaff && (
            <Button
              type="primary"
              icon={<PlusOutlined />}
              onClick={() => openEditor(null, 'runtime')}
            >
              新建智能体
            </Button>
          )}
          <Segmented
            value={source}
            onChange={(value) => setSource(value as SourceFilter)}
            options={[
              { label: '全部', value: 'all' },
              { label: '运行时智能体', value: 'runtime' },
              { label: '本地创作', value: 'local' },
            ]}
          />
        </div>
        <Search
          placeholder="搜索智能体..."
          prefix={<SearchOutlined className="text-text-dim" />}
          value={searchQuery}
          onChange={(e) => setSearchQuery(e.target.value)}
          allowClear
          className="max-w-xs"
        />
      </div>

      {isBusy && isEmpty ? (
        <div className="flex items-center justify-center py-20">
          <Spin size="large" />
        </div>
      ) : isEmpty ? (
        <div className="flex items-center justify-center py-20">
          <Empty description={<span className="text-text-sec">暂无智能体</span>} />
        </div>
      ) : (
        <div className="agent-grid">
          {pagedRuntime.map(runtimeCard)}
          {pagedLocal.map(localCard)}
        </div>
      )}

      {!isEmpty && (
        <div className="agents-page__more">
          {showRuntime && appsHasMore ? (
            <Button
              onClick={() => void loadMoreApps()}
              loading={loadingMoreApps}
            >
              加载更多智能体
            </Button>
          ) : (
            totalLoaded > PAGE_SIZE && (
              <span className="agents-page__count">已显示全部 {totalLoaded} 个</span>
            )
          )}
        </div>
      )}

      <AgentDetailModal
        agentId={selectedAgentId}
        open={modalOpen}
        onClose={() => { setModalOpen(false); setSelectedAgentId(null); }}
      />
      <AgentEditorModal
        agentId={editingAgentId}
        mode={editingMode}
        open={editorOpen}
        onClose={() => { setEditorOpen(false); setEditingAgentId(null); }}
        onSaved={async () => {
          // The editor reports no payload, so the refreshed page is the source
          // of truth for the grid. The bootstrap is re-fetched (it is the
          // constant-size payload) to keep the home shortcuts and the category
          // rails consistent — a create, rename or re-category is exactly what
          // it describes. No catalog download happens either way (执行报告 §15).
          await refreshAll();
          await reloadBootstrap(true);
        }}
      />
      <AgentAvatarModal
        agent={avatarAgent}
        open={Boolean(avatarAgent)}
        onClose={() => setAvatarAgent(null)}
        onSaved={async (updated) => {
          // 改头像 patches the three places the avatar lives — the page row,
          // the entity cache and the bootstrap groups — and downloads nothing
          // (执行报告 §15 "修改头像").
          patchAppAgent(updated.id, { avatar_url: updated.avatar_url });
          const cached = useApplicationEntityStore.getState().get(updated.id);
          if (cached) upsertEntity({ ...cached, avatar_url: updated.avatar_url });
          patchBootstrap(updated.id, { avatar_url: updated.avatar_url });
          setAvatarAgent((current) => (
            current && current.id === updated.id ? { ...current, ...updated } : current));
        }}
      />
    </div>
  );
};

export default AgentsPage;
