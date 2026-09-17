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
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Avatar, Button, Empty, Input, Popconfirm, Segmented, Spin, Tag, Tooltip, message,
} from 'antd';
import {
  DeleteOutlined, EditOutlined, PictureOutlined, PlusOutlined,
  SearchOutlined, StarFilled, StarOutlined,
} from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useAgentStore } from '@/stores/useAgentStore';
import { useApplicationCatalogStore } from '@/stores/useApplicationCatalogStore';
import { useAuthStore } from '@/stores/useAuthStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import AgentDetailModal from '@/components/Agents/AgentDetailModal';
import AgentEditorModal, { type AgentEditorMode } from '@/components/Agents/AgentEditorModal';
import AgentAvatarModal from '@/components/Agents/AgentAvatarModal';
import { api } from '@/services/api';
import {
  deleteAgentApplication,
  fetchAgentRuntimes,
  fetchManageableAgents,
  setDefaultAgent,
  type AgentRuntimeDescriptor,
  type ManagedAgent,
} from '@/services/runApi';
import './AgentsPage.css';

const { Search } = Input;

const AGENT_ICONS = ['🎬', '✍️', '🎙️', '✂️', '🎨', '🎵', '💡', '🔧'];

/**
 * How many cards the grid renders before 加载更多.
 *
 * The catalog is served whole by `GET /v2/applications` (no pagination), so
 * rendering it in one pass means one DOM node per application — on a catalog
 * polluted by integration-test rows (1800+) that is a multi-second freeze on
 * entry, which is what "进去非常卡" is. The filters stay client-side and are
 * applied to the FULL list first, so 加载更多 can never disagree with them.
 */
const PAGE_SIZE = 24;

/**
 * Cards fade in with a small stagger — but only for the first few.
 *
 * `.agent-card` is `animation: fadeIn .4s ... both`, and `both` holds the
 * FIRST keyframe (opacity: 0) for the whole `animation-delay`. An uncapped
 * `index * delay` therefore makes a large catalog look EMPTY: card #1300
 * would stay invisible for over a minute. Capping the stagger keeps the
 * entrance animation while making "how long until the grid is readable"
 * independent of how many agents exist.
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

  // Runtime agents (Applications + bindings).
  const [appAgents, setAppAgents] = useState<ManagedAgent[]>([]);
  const [loadingApps, setLoadingApps] = useState(false);
  const [runtimes, setRuntimes] = useState<AgentRuntimeDescriptor[]>([]);
  const [source, setSource] = useState<SourceFilter>('all');

  const [selectedAgentId, setSelectedAgentId] = useState<number | null>(null);
  const [modalOpen, setModalOpen] = useState(false);
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingAgentId, setEditingAgentId] = useState<number | null>(null);
  const [editingMode, setEditingMode] = useState<AgentEditorMode>('runtime');
  const [avatarAgent, setAvatarAgent] = useState<ManagedAgent | null>(null);
  const [deletingAgentId, setDeletingAgentId] = useState<number | null>(null);
  const [busyAppId, setBusyAppId] = useState<number | null>(null);
  /** How many cards of the filtered result the grid currently renders. */
  const [visibleCount, setVisibleCount] = useState(PAGE_SIZE);

  const reloadCatalog = useApplicationCatalogStore((state) => state.load);
  const openApplication = useWorkspaceStore((state) => state.openApplication);
  // 智能体接入是管理员能力：普通用户只消费市场。
  const isStaff = useAuthStore((state) => Boolean(state.user?.is_staff));
  const isFirstRun = useRef(true);

  const loadAppAgents = useCallback(async () => {
    setLoadingApps(true);
    try {
      setAppAgents(await fetchManageableAgents());
    } finally {
      setLoadingApps(false);
    }
  }, []);

  useEffect(() => { void loadAppAgents(); }, [loadAppAgents]);

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

  // A new result set always starts at page one: keeping a grown visibleCount
  // across a search would skip straight past the matches the user is looking
  // at, and shrinking the result (typing more) would leave a stale tail.
  useEffect(() => {
    setVisibleCount(PAGE_SIZE);
  }, [selectedCategory, searchQuery, source]);

  const refreshAll = useCallback(async () => {
    await Promise.all([
      loadAgents(useAgentStore.getState().selectedCategory || undefined),
      loadAppAgents(),
      reloadCatalog(true),
    ]);
  }, [loadAgents, loadAppAgents, reloadCatalog]);

  /** Runtime label per card, read from the runtime catalog (never hard-coded). */
  const labelFor = useCallback((agent: ManagedAgent) => {
    if (!agent.provider_key) return '未绑定运行时';
    return runtimes.find((item) => (
      item.key === `${agent.provider_key}:${agent.runtime_type}`
    ))?.label || agent.provider_key;
  }, [runtimes]);

  const visibleAppAgents = useMemo(() => {
    const keyword = searchQuery.trim().toLowerCase();
    return appAgents.filter((agent) => {
      if (selectedCategory && agent.category_slug !== selectedCategory) return false;
      if (!keyword) return true;
      return agent.name.toLowerCase().includes(keyword)
        || (agent.description || '').toLowerCase().includes(keyword);
    });
  }, [appAgents, searchQuery, selectedCategory]);

  const showRuntime = source !== 'local';
  const showLocal = source !== 'runtime';
  // Filter FIRST, page SECOND: `visibleCount` is a budget shared by the two
  // sections (as they are in one grid), so 加载更多 reveals the next slice of
  // the same filtered result instead of a different one.
  const pagedRuntime = showRuntime ? visibleAppAgents.slice(0, visibleCount) : [];
  const pagedLocal = showLocal
    ? agents.slice(0, Math.max(0, visibleCount - pagedRuntime.length))
    : [];
  const totalMatched = (showRuntime ? visibleAppAgents.length : 0)
    + (showLocal ? agents.length : 0);
  const hasMore = totalMatched > pagedRuntime.length + pagedLocal.length;

  const isBusy = isLoading || loadingApps;
  const isEmpty = (!showRuntime || visibleAppAgents.length === 0)
    && (!showLocal || agents.length === 0);

  const openEditor = (agentId: number | null, mode: AgentEditorMode) => {
    setEditingAgentId(agentId);
    setEditingMode(mode);
    setEditorOpen(true);
  };

  const openRuntimeAgent = async (agent: ManagedAgent) => {
    if (!agent.is_bound) {
      message.warning('该智能体还没有可用的运行时绑定，无法对话；请先编辑补全。');
      return;
    }
    // The workspace resolves the slug from the catalog mirror; refresh it so a
    // just-created agent opens on the first click.
    await reloadCatalog(true);
    openApplication(agent.id);
    navigate(`/chat/${agent.slug}`);
  };

  const handleSetDefault = async (agent: ManagedAgent, next: boolean) => {
    setBusyAppId(agent.id);
    try {
      await setDefaultAgent(agent.id, next);
      message.success(next
        ? `「${agent.name}」已设为工作台默认智能体`
        : `「${agent.name}」已取消默认智能体`);
      await refreshAll();
    } catch (error: any) {
      message.error(error?.response?.data?.detail || '设置默认智能体失败');
    } finally {
      setBusyAppId(null);
    }
  };

  const handleDeleteRuntimeAgent = async (agent: ManagedAgent) => {
    setBusyAppId(agent.id);
    try {
      await deleteAgentApplication(agent.id);
      message.success('智能体已删除');
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

  const runtimeCard = (agent: ManagedAgent, index: number) => (
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
              onClick={() => setAvatarAgent(agent)}
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
      {agent.avatar_url ? (
        <img
          className="agent-card-avatar"
          src={agent.avatar_url}
          alt={`${agent.name} 头像`}
        />
      ) : (
        <div className={`agent-card-icon icon-gradient-${(index % 6) + 1}`}>
          {agent.icon || '🤖'}
        </div>
      )}
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
          {hasMore ? (
            <Button
              onClick={() => setVisibleCount((current) => current + PAGE_SIZE)}
            >
              加载更多（还有 {totalMatched - pagedRuntime.length - pagedLocal.length} 个）
            </Button>
          ) : (
            totalMatched > PAGE_SIZE && (
              <span className="agents-page__count">已显示全部 {totalMatched} 个</span>
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
        onSaved={refreshAll}
      />
      <AgentAvatarModal
        agent={avatarAgent}
        open={Boolean(avatarAgent)}
        onClose={() => setAvatarAgent(null)}
        onSaved={async (updated) => {
          // Keep the card in sync without a full reload, then refresh the
          // catalog so the workspace switcher shows the new avatar too.
          setAppAgents((current) => current.map((item) => (
            item.id === updated.id ? { ...item, ...updated } : item)));
          setAvatarAgent((current) => (
            current && current.id === updated.id ? { ...current, ...updated } : current));
          await reloadCatalog(true);
        }}
      />
    </div>
  );
};

export default AgentsPage;
