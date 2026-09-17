import React, { useMemo } from 'react';
import { StarFilled, StarOutlined } from '@ant-design/icons';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import type { ApplicationSummary } from '@/services/runApi';
import AgentAvatar from '@/components/Agents/AgentAvatar';

interface Props {
  /**
   * The groups, computed SERVER-side (执行报告 §11, P1-1).
   *
   * This component used to derive them from the whole catalog
   * (`buildShortcutGroups(applications)`), which is precisely why the shell
   * downloaded every application on mount. The server applies the same rules
   * (收藏 / 常用 / 最近使用 / 推荐 / 常用应用) to the same fields, so the
   * rendering below is unchanged — only the source of truth moved.
   */
  favorites: ApplicationSummary[];
  frequent: ApplicationSummary[];
  recent: ApplicationSummary[];
  recommended: ApplicationSummary[];
  /** 常用应用: non-chat applications, recently opened first. */
  recentFixedApps: ApplicationSummary[];
  onOpen: (application: ApplicationSummary) => void;
  onToggleFavorite: (applicationId: number) => void;
}

const RECENT_LIMIT = 8;

/**
 * HomeShortcuts — 常用智能体 / 常用应用 / 最近使用 / 收藏 (§35).
 *
 * Everything here is an Application; there is no "agent shortcut" vs
 * "app shortcut" distinction (§35).
 */
const HomeShortcuts: React.FC<Props> = ({
  favorites, frequent, recent: serverRecent, recommended, recentFixedApps,
  onOpen, onToggleFavorite,
}) => {
  const recentApplicationIds = useWorkspaceStore((state) => state.recentApplicationIds);
  // Local recency (the shell's own navigation log) still leads: it reflects
  // what the user did in THIS session, including applications the server has
  // no usage row for yet. The rows it points at are the ones the entity cache
  // resolved while navigating — one lookup, no catalog.
  const entities = useApplicationEntityStore((state) => state.byId);

  const recent = useMemo(() => {
    const local = recentApplicationIds
      .map((id) => entities[id])
      .filter((app) => app != null && app.kind === 'chat');
    const merged: ApplicationSummary[] = [];
    const seen = new Set<number>();
    for (const app of [...local, ...serverRecent]) {
      if (seen.has(app.id)) continue;
      seen.add(app.id);
      merged.push(app);
    }
    return merged.slice(0, RECENT_LIMIT);
  }, [recentApplicationIds, entities, serverRecent]);

  const fixedApplications = useMemo(
    () => recentFixedApps.slice(0, RECENT_LIMIT),
    [recentFixedApps]);

  const sections = useMemo(() => [
    { key: 'favorites', label: '收藏', items: favorites },
    { key: 'frequent', label: '常用智能体', items: frequent },
    { key: 'applications', label: '常用应用', items: fixedApplications },
    { key: 'recent', label: '最近使用', items: recent },
    { key: 'recommended', label: '推荐', items: recommended },
  ].filter((section) => section.items.length > 0),
  [favorites, fixedApplications, frequent, recommended, recent]);

  const hasAnything = favorites.length > 0 || frequent.length > 0
    || recent.length > 0 || recommended.length > 0 || fixedApplications.length > 0;

  if (!hasAnything) {
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
