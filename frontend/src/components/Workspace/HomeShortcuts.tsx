import React, { useMemo } from 'react';
import { StarFilled, StarOutlined } from '@ant-design/icons';
import { buildShortcutGroups } from '@/stores/useApplicationCatalogStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import type { V2Application } from '@/services/runApi';
import AgentAvatar from '@/components/Agents/AgentAvatar';

interface Props {
  applications: V2Application[];
  onOpen: (application: V2Application) => void;
  onToggleFavorite: (applicationId: number) => void;
}

const RECENT_LIMIT = 8;

/**
 * HomeShortcuts — 常用智能体 / 常用应用 / 最近使用 / 收藏 (§35).
 *
 * Everything here is an Application; there is no "agent shortcut" vs
 * "app shortcut" distinction (§35). Data comes from the catalog endpoint,
 * merged with the local recency the shell keeps while navigating.
 */
const HomeShortcuts: React.FC<Props> = ({
  applications, onOpen, onToggleFavorite,
}) => {
  const recentApplicationIds = useWorkspaceStore((state) => state.recentApplicationIds);
  // 停用的应用（应用中心开关）对普通用户已被服务端过滤；这里兜底过滤掉
  // 管理员视角下混入目录的停用应用，保持首页快捷入口干净。
  const enabledApplications = useMemo(
    () => applications.filter((app) => app.enabled !== false),
    [applications]);
  const groups = useMemo(() => buildShortcutGroups(enabledApplications), [enabledApplications]);

  const recent = useMemo(() => {
    const local = recentApplicationIds
      .map((id) => enabledApplications.find((app) => app.id === id))
      .filter((app): app is V2Application => app != null && app.kind === 'chat');
    const merged: V2Application[] = [];
    const seen = new Set<number>();
    for (const app of [...local, ...groups.recent]) {
      if (seen.has(app.id)) continue;
      seen.add(app.id);
      merged.push(app);
    }
    return merged.slice(0, RECENT_LIMIT);
  }, [recentApplicationIds, enabledApplications, groups.recent]);

  const fixedApplications = useMemo(
    () => enabledApplications.filter((app) => app.kind !== 'chat').slice(0, RECENT_LIMIT),
    [enabledApplications]);

  const sections = useMemo(() => [
    { key: 'favorites', label: '收藏', items: groups.favorites },
    { key: 'frequent', label: '常用智能体', items: groups.frequent },
    { key: 'applications', label: '常用应用', items: fixedApplications },
    { key: 'recent', label: '最近使用', items: recent },
    { key: 'recommended', label: '推荐', items: groups.recommended },
  ].filter((section) => section.items.length > 0),
  [fixedApplications, groups.favorites, groups.frequent, groups.recommended, recent]);

  if (!enabledApplications.length) {
    return (
      <div className="home-shortcuts home-shortcuts--empty">
        <p>暂无可用的智能体与应用，请联系管理员在智能体市场或应用中心配置。</p>
      </div>
    );
  }

  return (
    <div className="home-shortcuts">
      {sections.map((section) => (
        <section className="home-shortcuts__group" key={section.key}>
          <div className="home-shortcuts__label">{section.label}</div>
          <div className="home-shortcuts__grid">
            {section.items.map((app) => (
              <div
                key={`${section.key}-${app.id}`}
                className="home-shortcut"
                role="button"
                tabIndex={0}
                onClick={() => onOpen(app)}
                onKeyDown={(event) => {
                  if (event.key === 'Enter' || event.key === ' ') {
                    event.preventDefault();
                    onOpen(app);
                  }
                }}
              >
                <AgentAvatar
                  application={app}
                  size={34}
                  className="home-shortcut__icon"
                  tint={app.color}
                />
                <span className="home-shortcut__body">
                  <span className="home-shortcut__name">{app.name}</span>
                  {app.description && (
                    <span className="home-shortcut__desc">{app.description}</span>
                  )}
                </span>
                <button
                  type="button"
                  className="home-shortcut__star"
                  aria-label={app.is_favorite ? `取消收藏 ${app.name}` : `收藏 ${app.name}`}
                  onClick={(event) => {
                    event.stopPropagation();
                    onToggleFavorite(app.id);
                  }}
                >
                  {app.is_favorite ? <StarFilled /> : <StarOutlined />}
                </button>
              </div>
            ))}
          </div>
        </section>
      ))}
    </div>
  );
};

export default HomeShortcuts;
