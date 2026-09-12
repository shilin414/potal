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
  const groups = useMemo(() => buildShortcutGroups(applications), [applications]);

  const recent = useMemo(() => {
    const local = recentApplicationIds
      .map((id) => applications.find((app) => app.id === id))
      .filter((app): app is V2Application => app != null && app.kind === 'chat');
    const merged: V2Application[] = [];
    const seen = new Set<number>();
    for (const app of [...local, ...groups.recent]) {
      if (seen.has(app.id)) continue;
      seen.add(app.id);
      merged.push(app);
    }
    return merged.slice(0, RECENT_LIMIT);
  }, [recentApplicationIds, applications, groups.recent]);

  const fixedApplications = useMemo(
    () => applications.filter((app) => app.kind !== 'chat').slice(0, RECENT_LIMIT),
    [applications]);

  const sections = useMemo(() => [
    { key: 'favorites', label: '收藏', items: groups.favorites },
    { key: 'frequent', label: '常用智能体', items: groups.frequent },
    { key: 'applications', label: '常用应用', items: fixedApplications },
    { key: 'recent', label: '最近使用', items: recent },
    { key: 'recommended', label: '推荐', items: groups.recommended },
  ].filter((section) => section.items.length > 0),
  [fixedApplications, groups.favorites, groups.frequent, groups.recommended, recent]);

  if (!applications.length) {
    return (
      <div className="home-shortcuts home-shortcuts--empty">
        <p>还没有可用的智能体，先到应用中心添加一个吧。</p>
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
