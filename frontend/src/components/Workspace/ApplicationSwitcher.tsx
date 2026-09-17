/**
 * ApplicationSwitcher — 智能体快速切换 (§31).
 *
 * Renders `销售助手 ▼` and lets the user jump between agents without going
 * back to the application center. Switching only changes the active
 * application; AppShell stays mounted and each workspace restores its own
 * conversation/draft/scroll from workspaceStore (§29).
 */
import { useEffect, useMemo } from 'react';
import { Dropdown, Spin } from 'antd';
import { DownOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useApplicationCatalogStore } from '@/stores/useApplicationCatalogStore';
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
  // The store keeps one already-derived chat list instead of every consumer
  // re-filtering the 1800-row catalog on each render — see
  // `chatApplicationsList` in useApplicationCatalogStore.
  const chats = useApplicationCatalogStore((state) => state.chatApplicationsList);
  const isLoading = useApplicationCatalogStore((state) => state.isLoading);
  const load = useApplicationCatalogStore((state) => state.load);
  const activeApplicationId = useWorkspaceStore((state) => state.activeApplicationId);
  const recentApplicationIds = useWorkspaceStore((state) => state.recentApplicationIds);
  const openApplication = useWorkspaceStore((state) => state.openApplication);

  useEffect(() => { void load(); }, [load]);

  const active = chats.find((app) => app.id === activeApplicationId) || null;

  const open = (application: V2Application) => {
    openApplication(application.id);
    navigate(`/chat/${application.slug}`);
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
    .map((id) => chats.find((app) => app.id === id))
    .filter((app): app is V2Application => Boolean(app));

  // `GET /v2/applications` has no server-side cap, and integration-test rows
  // have pushed the shared catalog past 1800 — building a Menu row per entry
  // is what makes opening the switcher (and the shell that renders it) crawl.
  // This dropdown is a JUMP-TO menu: browsing the whole catalog is the market
  // page behind 全部智能体 >, so the full group is bounded. The agent in play
  // is pinned so it can never be the one that got cut.
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
      disabled={isLoading && !chats.length}
    >
      <button
        type="button"
        className={`application-switcher ${compact ? 'application-switcher--compact' : ''}`}
        aria-label="切换智能体"
      >
        {isLoading && !chats.length ? (
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
