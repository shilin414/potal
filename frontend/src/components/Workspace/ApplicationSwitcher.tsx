/**
 * ApplicationSwitcher — 智能体快速切换 (§31).
 *
 * Renders `销售助手 ▼` and lets the user jump between agents without going
 * back to the application center. Switching only changes the active
 * application; AppShell stays mounted and each workspace restores its own
 * conversation/draft/scroll from workspaceStore (§29).
 *
 * Data source (执行报告 §9.3): the switcher used to render its rows from the
 * whole-catalog mirror, so "50 DOM rows" still meant "1800 rows downloaded".
 * It now reads ONE server page of the paged endpoint — the same budget it
 * renders — and resolves the active agent from the entity cache.
 */
import { useMemo } from 'react';
import { Dropdown, Spin, message } from 'antd';
import { DownOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useApplicationPage } from '@/hooks/useApplicationPage';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import type { V2Application } from '@/services/runApi';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import './ApplicationSwitcher.css';

interface Props {
  /** Compact styling for the mobile top bar (§21). */
  compact?: boolean;
}

/** How many rows the 全部智能体 group builds (see fullList below). */
const SWITCHER_MAX_ROWS = 50;

const ApplicationSwitcher: React.FC<Props> = ({ compact }) => {
  const navigate = useNavigate();
  // One server page, capped at what this dropdown can render: the browser
  // holds 50 agent rows, not the catalog.
  const {
    items: chats,
    loading,
  } = useApplicationPage({
    kind: 'chat',
    scope: 'accessible',
    // consume (二次复审 P0-5): this dropdown SWITCHES the workspace to an
    // agent, so it may only offer applications that can actually run. With
    // manage scope alone a staff user saw 停用 and binding-less agents here
    // and could pick one whose first message the run API then refused.
    mode: 'consume',
    includeUnbound: false,
    limit: SWITCHER_MAX_ROWS,
  });
  const entities = useApplicationEntityStore((state) => state.byId);
  const activeApplicationId = useWorkspaceStore((state) => state.activeApplicationId);
  const recentApplicationIds = useWorkspaceStore((state) => state.recentApplicationIds);
  const openApplication = useWorkspaceStore((state) => state.openApplication);
  const ensure = useApplicationEntityStore((state) => state.ensure);

  // The active agent may be off this page (unbound, or beyond row 50): the
  // entity cache still knows it because entering it is what resolved it.
  const active = (activeApplicationId != null ? entities[activeApplicationId] : undefined)
    || chats.find((app) => app.id === activeApplicationId)
    || null;

  const open = async (application: V2Application) => {
    try {
      // 最近使用 may come from the entity cache, which can predate an admin
      // disable (四次复审 P1-R1). Revalidate before navigating so the user
      // gets "应用当前不可用" instead of entering a workspace whose Send fails.
      const current = await ensure(application.id, { maxAgeMs: 0 });
      if (!current) {
        message.error('应用当前不可用');
        return;
      }
      openApplication(application.id);
      navigate(`/chat/${application.slug}`);
    } catch {
      // Transport failure: keep the route decision with WorkspaceHost, which
      // renders its own retry state instead of pretending the agent vanished.
      openApplication(application.id);
      navigate(`/chat/${application.slug}`);
    }
  };

  // 最近使用 and 全部智能体 render the SAME applications, so one shared key
  // would appear twice in a single Menu — React warns, and antd would treat
  // the two rows as one entry for active-key tracking. The fix is to scope
  // the key by section, NOT to drop the repeat: 全部智能体 must stay complete,
  // that is the whole point of the group.
  const itemFor = (application: V2Application, section: string) => ({
    key: `${section}-${application.id}`,
    label: (
      <span className="application-switcher__item">
        <AgentAvatar
          application={application}
          size={18}
          shape="circle"
          className="application-switcher__item-icon"
        />
        <span className="application-switcher__item-name">{application.name}</span>
        {application.is_default_agent && (
          <span className="application-switcher__main" title="工作台默认主智能体">
            主
          </span>
        )}
        {application.id === activeApplicationId && (
          <span className="application-switcher__tick" aria-label="当前">✓</span>
        )}
      </span>
    ),
    onClick: () => open(application),
  });

  const recent = recentApplicationIds
    .map((id) => entities[id] || chats.find((app) => app.id === id))
    .filter((app): app is V2Application => Boolean(app));

  // This dropdown is a JUMP-TO menu: browsing the whole catalog is the market
  // page behind 全部智能体 >, so the full group is bounded — and now the
  // REQUEST is bounded too (one page of 50). The agent in play is pinned so it
  // can never be the one that got cut.
  const fullList = useMemo(() => {
    const head = chats.slice(0, SWITCHER_MAX_ROWS);
    if (!active || head.some((app) => app.id === active.id)) return head;
    return [active, ...head];
  }, [chats, active]);

  const menuItems: any[] = [];
  if (recent.length) {
    menuItems.push({
      type: 'group' as const,
      label: '最近使用',
      children: recent.map((app) => itemFor(app, 'recent')),
    });
  }
  menuItems.push({
    type: 'group' as const,
    label: recent.length ? '全部智能体' : '智能体',
    children: fullList.map((app) => itemFor(app, 'all')),
  });
  menuItems.push({ type: 'divider' as const });
  menuItems.push({
    key: '__all__',
    label: '全部智能体 >',
    onClick: () => navigate('/agents'),
  });

  return (
    <Dropdown
      menu={{ items: menuItems }}
      trigger={['click']}
      placement="bottomLeft"
      disabled={loading && !chats.length}
    >
      <button
        type="button"
        className={`application-switcher ${compact ? 'application-switcher--compact' : ''}`}
        aria-label="切换智能体"
      >
        {loading && !chats.length ? (
          <Spin size="small" />
        ) : (
          <>
            {active ? (
              <AgentAvatar
                application={active}
                size={20}
                shape="circle"
                className="application-switcher__icon"
              />
            ) : (
              <span className="application-switcher__icon">✦</span>
            )}
            <span className="application-switcher__name">
              {active?.name || '选择智能体'}
            </span>
            <DownOutlined className="application-switcher__caret" />
          </>
        )}
      </button>
    </Dropdown>
  );
};

export default ApplicationSwitcher;
