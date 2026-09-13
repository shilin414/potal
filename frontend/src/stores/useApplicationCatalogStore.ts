/**
 * useApplicationCatalogStore — the shell's mirror of the application catalog.
 *
 * Data comes from `GET /api/v2/applications` (backend is the source of truth);
 * this store only caches it so the WorkspaceHost can resolve a slug, render
 * home shortcuts and flip favourites without refetching on every render.
 *
 * Provider differences never appear here: the backend already reduced them to
 * binding capabilities / renderer keys (architecture doc §15/§100).
 */
import { create } from 'zustand';
import {
  fetchV2Applications,
  setApplicationFavorite,
} from '@/services/runApi';
import type { V2Application } from '@/services/runApi';

export interface ShortcutGroups {
  favorites: V2Application[];
  frequent: V2Application[];
  recent: V2Application[];
  recommended: V2Application[];
}

const byName = (a: V2Application, b: V2Application) =>
  a.name.localeCompare(b.name, 'zh-Hans-CN');

const lastUsed = (app: V2Application): number =>
  app.last_used_at ? Date.parse(app.last_used_at) || 0 : 0;

/**
 * Derive the home workspace shortcut groups (§35). Pure so it is unit
 * tested without a live backend.
 *
 * - 收藏: explicit pins.
 * - 常用: applications this user actually ran, most used first.
 * - 最近使用: most recently run.
 * - 推荐: applications this user has NOT used yet, most popular first — without
 *   it a newly published agent would be invisible to anyone with history.
 */
export function buildShortcutGroups(
  applications: V2Application[],
  limit = 8,
): ShortcutGroups {
  const chat = applications.filter((app) => app.kind === 'chat');
  const favorites = chat.filter((app) => app.is_favorite).sort(
    (a, b) => lastUsed(b) - lastUsed(a) || byName(a, b));

  const frequent = chat
    .filter((app) => (app.usage_count || 0) > 0)
    .sort((a, b) => (b.usage_count || 0) - (a.usage_count || 0)
      || lastUsed(b) - lastUsed(a) || byName(a, b))
    .slice(0, limit);

  const recent = chat.filter((app) => lastUsed(app) > 0)
    .sort((a, b) => lastUsed(b) - lastUsed(a))
    .slice(0, limit);

  // 推荐 means "not used yet", so it must consider EVERY used application —
  // not just the ones that survived the per-group cap above.
  const known = new Set<number>();
  chat.forEach((app) => {
    if ((app.usage_count || 0) > 0 || lastUsed(app) > 0 || app.is_favorite) {
      known.add(app.id);
    }
  });
  const recommended = chat
    .filter((app) => !known.has(app.id))
    .sort((a, b) => (b.global_usage_count || 0) - (a.global_usage_count || 0)
      || byName(a, b))
    .slice(0, limit);

  return { favorites, frequent, recent, recommended };
}

/**
 * The workspace's default main agent (§38).
 *
 * Prefers the application explicitly marked as the main agent; falls back to
 * the first bound chat application so a fresh install still works without
 * configuration.
 *
 * Pure, so the home composer and the chat panel cannot drift apart.
 */
export function resolveDefaultApplication(
  applications: V2Application[],
): V2Application | undefined {
  const chats = applications.filter(
    (app) => app.kind === 'chat' && app.is_bound !== false);
  return chats.find((app) => app.is_default_agent) || chats[0];
}

interface CatalogState {
  applications: V2Application[];
  isLoading: boolean;
  error: string | null;
  loadedAt: number;
  /** 应用中心的分类筛选（侧栏与卡片网格共享）。 */
  fixedCategory: string | null;

  load: (force?: boolean) => Promise<V2Application[]>;
  toggleFavorite: (applicationId: number) => Promise<void>;
  setFixedCategory: (category: string | null) => void;
  applicationById: (applicationId: number | null | undefined) => V2Application | undefined;
  applicationBySlug: (slug: string | undefined) => V2Application | undefined;
  /** Slug-or-id lookup, used by the legacy `/apps/:id/run` redirect. */
  applicationByIdentifier: (identifier: string | undefined) => V2Application | undefined;
  chatApplications: () => V2Application[];
  fixedApplications: () => V2Application[];
  clear: () => void;
}

let inflight: Promise<V2Application[]> | null = null;

export const useApplicationCatalogStore = create<CatalogState>()((set, get) => ({
  applications: [],
  isLoading: false,
  error: null,
  loadedAt: 0,
  fixedCategory: null,

  load: async (force = false) => {
    if (!force && get().loadedAt) return get().applications;
    if (inflight) return inflight;
    set({ isLoading: true, error: null });
    // scope=manage so a user's own (possibly private) agents reach the
    // switcher and the home shortcuts; bound-only semantics are unchanged
    // because unbound chat apps are excluded unless include_unbound is set.
    inflight = fetchV2Applications('all', { scope: 'manage' })
      .then((applications) => {
        set({ applications, isLoading: false, loadedAt: Date.now() });
        return applications;
      })
      .catch((error: any) => {
        set({
          error: error?.response?.data?.detail || '应用列表加载失败',
          isLoading: false,
        });
        return [] as V2Application[];
      })
      .finally(() => { inflight = null; });
    return inflight;
  },

  toggleFavorite: async (applicationId) => {
    const current = get().applications.find((app) => app.id === applicationId);
    if (!current) return;
    const next = !current.is_favorite;
    // Optimistic: the shell must react instantly; revert on failure.
    set({
      applications: get().applications.map((app) => (
        app.id === applicationId ? { ...app, is_favorite: next } : app)),
    });
    try {
      const result = await setApplicationFavorite(applicationId, next);
      set({
        applications: get().applications.map((app) => (
          app.id === applicationId
            ? { ...app, is_favorite: result.is_favorite } : app)),
      });
    } catch {
      set({
        applications: get().applications.map((app) => (
          app.id === applicationId
            ? { ...app, is_favorite: current.is_favorite } : app)),
        error: '收藏操作失败',
      });
    }
  },

  applicationById: (applicationId) => (
    applicationId == null
      ? undefined
      : get().applications.find((app) => app.id === applicationId)),

  setFixedCategory: (fixedCategory) => set({ fixedCategory }),

  applicationBySlug: (slug) => (
    slug ? get().applications.find((app) => app.slug === slug) : undefined),

  applicationByIdentifier: (identifier) => {
    if (!identifier) return undefined;
    const numeric = Number(identifier);
    return get().applications.find((app) => (
      String(app.id) === identifier || app.slug === identifier
      || (!Number.isNaN(numeric) && app.id === numeric)));
  },

  chatApplications: () => get().applications.filter((app) => app.kind === 'chat'),
  fixedApplications: () => get().applications.filter((app) => app.kind !== 'chat'),

  clear: () => set({ applications: [], loadedAt: 0, error: null }),
}));
