/**
 * useWorkspaceBootstrapStore — the shell's START-UP facts (执行报告 §9–§11, P1-1).
 *
 * It replaces `useApplicationCatalogStore`, whose one `load()` fetched the
 * WHOLE catalog (1800+ rows on the shared dev DB) so the shell could resolve
 * a slug, pick the default main agent, render home shortcuts and list the
 * category rails. That mirror was the last place where "the pages are
 * paginated but the shell still downloads everything" was true.
 *
 * This store holds what the shell genuinely needs at start-up, and nothing
 * else:
 *
 *   defaultApplication  the composer's bound agent (§38)
 *   favorites / frequent / recent / recommended / recentFixedApps
 *                       the home shortcut groups (§35)
 *   agentCategories / appCategories
 *                       the category rails (智能体市场 / 应用中心 / 移动端)
 *
 * That is ~30 rows regardless of how large the catalog is, and the payload is
 * computed server-side (GET /api/v2/workspace/bootstrap) so no client-side
 * projection over a huge array exists any more.
 *
 * Applications NOT in this payload are resolved one at a time through
 * `useApplicationEntityStore` — see that store's header.
 */
import { create } from 'zustand';
import {
  fetchWorkspaceBootstrap,
  setApplicationFavorite,
  type ApplicationCategory,
  type ApplicationSummary,
  type ComposerApplication,
} from '@/services/runApi';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';

export interface BootstrapState {
  defaultApplication: ComposerApplication | null;
  favorites: ApplicationSummary[];
  frequent: ApplicationSummary[];
  recent: ApplicationSummary[];
  recommended: ApplicationSummary[];
  recentFixedApps: ApplicationSummary[];
  agentCategories: ApplicationCategory[];
  appCategories: ApplicationCategory[];
  isLoading: boolean;
  error: string | null;
  loadedAt: number;

  /** Fetch once; `force` re-fetches (after a create/delete/default change). */
  load: (force?: boolean) => Promise<void>;
  /** Local projection of one row across every group it appears in. */
  patch: (id: number, patch: Partial<ApplicationSummary>) => void;
  /** Drop one row from every group (delete). */
  remove: (id: number) => void;
  /**
   * Record a favourite toggle (optimistic) — the ✩ on the home shortcuts, the
   * chat header and the fixed-app header.
   *
   * `fallback` is the caller's own entity when the application is NOT part of
   * the bootstrap payload (a freshly created agent, or one that is not in any
   * shortcut group): without it the toggle would silently do nothing, because
   * there is no group row to flip. The entity cache is patched too, so the
   * ✩ that reads `application.is_favorite` off a resolved route updates.
   */
  toggleFavorite: (
    applicationId: number,
    fallback?: ApplicationSummary,
  ) => Promise<void>;
  /** Every row this payload knows about, deduplicated (lookup fallback). */
  summaryById: (id: number | null | undefined) => ApplicationSummary | undefined;
  summaryBySlug: (slug: string | undefined) => ApplicationSummary | undefined;
  clear: () => void;
}

const EMPTY: ApplicationSummary[] = [];
const EMPTY_CATEGORIES: ApplicationCategory[] = [];
let inflight: Promise<void> | null = null;

const patchList = (
  list: ApplicationSummary[],
  id: number,
  patch: Partial<ApplicationSummary>,
): ApplicationSummary[] => list.map((item) => (
  item.id === id ? { ...item, ...patch } : item));

/**
 * A favourite toggle moves a chat application IN or OUT of the 收藏 group.
 * Keeping the membership honest locally (rather than refetching the whole
 * bootstrap for one boolean) is what the report asks for (§15 "patch
 * bootstrap 中出现的同 id item"), and it keeps the ✩ instant.
 */
const applyFavorite = (
  list: ApplicationSummary[],
  id: number,
  favorite: boolean,
  source: ApplicationSummary | undefined,
): ApplicationSummary[] => {
  const without = list.filter((item) => item.id !== id);
  if (!favorite || !source) return without;
  return [...without, { ...source, is_favorite: true }];
};

export const useWorkspaceBootstrapStore = create<BootstrapState>()((set, get) => ({
  defaultApplication: null,
  favorites: EMPTY,
  frequent: EMPTY,
  recent: EMPTY,
  recommended: EMPTY,
  recentFixedApps: EMPTY,
  agentCategories: EMPTY_CATEGORIES,
  appCategories: EMPTY_CATEGORIES,
  isLoading: false,
  error: null,
  loadedAt: 0,

  load: async (force = false) => {
    if (!force && get().loadedAt) return;
    if (inflight) return inflight;
    set({ isLoading: true, error: null });
    inflight = fetchWorkspaceBootstrap()
      .then((payload) => {
        set({
          defaultApplication: payload.default_application ?? null,
          favorites: payload.favorites ?? EMPTY,
          frequent: payload.frequent ?? EMPTY,
          recent: payload.recent ?? EMPTY,
          recommended: payload.recommended ?? EMPTY,
          recentFixedApps: payload.recent_fixed_apps ?? EMPTY,
          agentCategories: payload.agent_categories ?? EMPTY_CATEGORIES,
          appCategories: payload.app_categories ?? EMPTY_CATEGORIES,
          isLoading: false,
          loadedAt: Date.now(),
        });
      })
      .catch((error: any) => {
        set({
          error: error?.response?.data?.detail || '工作台数据加载失败',
          isLoading: false,
        });
      })
      .finally(() => { inflight = null; });
    return inflight;
  },

  patch: (id, patch) => set((state) => ({
    defaultApplication: state.defaultApplication?.id === id
      ? { ...state.defaultApplication, ...patch }
      : state.defaultApplication,
    favorites: patchList(state.favorites, id, patch),
    frequent: patchList(state.frequent, id, patch),
    recent: patchList(state.recent, id, patch),
    recommended: patchList(state.recommended, id, patch),
    recentFixedApps: patchList(state.recentFixedApps, id, patch),
  })),

  remove: (id) => set((state) => ({
    defaultApplication: state.defaultApplication?.id === id
      ? null
      : state.defaultApplication,
    favorites: state.favorites.filter((item) => item.id !== id),
    frequent: state.frequent.filter((item) => item.id !== id),
    recent: state.recent.filter((item) => item.id !== id),
    recommended: state.recommended.filter((item) => item.id !== id),
    recentFixedApps: state.recentFixedApps.filter((item) => item.id !== id),
  })),

  toggleFavorite: async (applicationId, fallback) => {
    const state = get();
    const known = state.summaryById(applicationId) ?? fallback;
    // Nothing to flip and nothing to patch: the caller has no row at all.
    if (!known) return;
    const next = !known.is_favorite;
    const before = {
      favorites: state.favorites,
      defaultApplication: state.defaultApplication,
      frequent: state.frequent,
      recent: state.recent,
      recommended: state.recommended,
      recentFixedApps: state.recentFixedApps,
    };
    const write = (favorite: boolean) => {
      set((s) => ({
        favorites: applyFavorite(s.favorites, applicationId, favorite, known),
        defaultApplication: s.defaultApplication?.id === applicationId
          ? { ...s.defaultApplication, is_favorite: favorite }
          : s.defaultApplication,
        frequent: patchList(s.frequent, applicationId, { is_favorite: favorite }),
        recent: patchList(s.recent, applicationId, { is_favorite: favorite }),
        recommended: patchList(s.recommended, applicationId, { is_favorite: favorite }),
        recentFixedApps: patchList(s.recentFixedApps, applicationId, { is_favorite: favorite }),
      }));
      // The route-resolved entity carries the flag the ✩ in a chat header
      // reads, so it has to follow along (patch is a no-op when not cached).
      useApplicationEntityStore.getState().patch(applicationId, { is_favorite: favorite });
    };
    write(next);
    try {
      const result = await setApplicationFavorite(applicationId, next);
      // The server is authoritative about the flag it actually stored.
      if (result.is_favorite !== next) write(result.is_favorite);
    } catch {
      // Revert to the exact previous collections (an absent flag means "not
      // favourited", which is what `?? false` restores).
      set({ ...before, error: '收藏操作失败' });
      useApplicationEntityStore.getState().patch(applicationId, {
        is_favorite: known.is_favorite ?? false,
      });
    }
  },

  summaryById: (id) => {
    if (id == null) return undefined;
    const state = get();
    for (const list of [
      state.favorites, state.frequent, state.recent,
      state.recommended, state.recentFixedApps,
    ]) {
      const hit = list.find((item) => item.id === id);
      if (hit) return hit;
    }
    return state.defaultApplication?.id === id ? state.defaultApplication : undefined;
  },

  summaryBySlug: (slug) => {
    if (!slug) return undefined;
    const state = get();
    for (const list of [
      state.favorites, state.frequent, state.recent,
      state.recommended, state.recentFixedApps,
    ]) {
      const hit = list.find((item) => item.slug === slug);
      if (hit) return hit;
    }
    return state.defaultApplication?.slug === slug ? state.defaultApplication : undefined;
  },

  clear: () => set({
    defaultApplication: null,
    favorites: EMPTY,
    frequent: EMPTY,
    recent: EMPTY,
    recommended: EMPTY,
    recentFixedApps: EMPTY,
    loadedAt: 0,
    error: null,
  }),
}));
