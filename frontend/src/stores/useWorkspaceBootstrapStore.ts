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
 *
 * ── Two rules this store enforces (二次复审 P1-4 / P0-2) ──
 *
 * 1. FRESHNESS. `loadedAt` alone meant "fresh forever": chat with an agent,
 *    walk back to the home page, and 常用 / 最近使用 / 推荐 were still the
 *    values from page load (usage_count and last_used_at had moved on the
 *    server). `dirty` is a mark, not a refetch: mutations call
 *    `invalidate()`, and the next surface that NEEDS the groups re-reads
 *    them. No per-message bootstrap request.
 *
 * 2. IDENTITY. `clear()` bumps `generation`. A bootstrap request that was
 *    already in flight when the user switched cannot write afterwards: its
 *    generation no longer matches, so its response is dropped. Without this,
 *    clearing the store would not be enough — A's response would land on
 *    B's session (P0-2 §6).
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
import { registerSessionReset } from '@/stores/resetSessionState';

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
  /** Set by `invalidate()`: the groups are known to be out of date. */
  dirty: boolean;

  /** Fetch once; `force` re-fetches (after a create/delete/default change). */
  load: (force?: boolean) => Promise<void>;
  /**
   * Mark the groups stale (二次复审 P1-4) instead of refetching.
   *
   * Called after anything that moves a group membership the client cannot
   * recompute: a run, an enable/public toggle, a create/edit/delete, a
   * category change, a default-agent change, a favourite toggle.
   */
  invalidate: () => void;
  /** Local projection of one row across every group it appears in. */
  patch: (id: number, patch: Partial<ApplicationSummary>) => void;
  /** Drop one row from every group (delete / 停用). */
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

/**
 * The ONE definition of "empty".
 *
 * `clear()` used to hand-pick six of the nine fields, which left
 * `agentCategories`, `appCategories` and `isLoading` carrying the PREVIOUS
 * user's values across a logout (二次复审 §7). Two lists of defaults is how
 * that happens, so there is now one.
 */
const initialBootstrapState = {
  defaultApplication: null,
  favorites: [] as ApplicationSummary[],
  frequent: [] as ApplicationSummary[],
  recent: [] as ApplicationSummary[],
  recommended: [] as ApplicationSummary[],
  recentFixedApps: [] as ApplicationSummary[],
  agentCategories: [] as ApplicationCategory[],
  appCategories: [] as ApplicationCategory[],
  isLoading: false,
  error: null as string | null,
  loadedAt: 0,
  dirty: false,
};

let inflight: Promise<void> | null = null;
/**
 * Bumped by `clear()` (二次复审 P0-2 §6). A response whose generation is no
 * longer current belongs to a session that has already ended and is dropped
 * instead of being written.
 */
let generation = 0;

const patchList = (
  list: ApplicationSummary[],
  id: number,
  patch: Partial<ApplicationSummary>,
): ApplicationSummary[] => list.map((item) => (
  item.id === id ? { ...item, ...patch } : item));

/** Mirrors the backend's per-group budget (§35). */
const BOOTSTRAP_GROUP_LIMIT = 8;

/**
 * A favourite toggle moves a CHAT application IN or OUT of the 收藏 group
 * (二次复审 P1-5).
 *
 * The backend's 收藏 group is defined as `chat + starred`. The previous
 * version applied the membership change for ANY kind, so starring a fixed
 * application (「OA 密码修改」) pushed it into 「收藏智能体」 — until the
 * next server bootstrap corrected it. A fixed application only flips the ✩
 * (`is_favorite` in 常用应用 + in the entity cache); it is never a member
 * of the agent group.
 *
 * The group is re-sorted and capped with the server's own ordering
 * (`last_used_at DESC, name`, LIMIT 8) so a 9th star cannot leave the client
 * rendering nine rows.
 */
const applyFavorite = (
  list: ApplicationSummary[],
  id: number,
  favorite: boolean,
  source: ApplicationSummary | undefined,
): ApplicationSummary[] => {
  const without = list.filter((item) => item.id !== id);
  if (!favorite || !source) return without;
  const next = [...without, { ...source, is_favorite: true }];
  next.sort((a, b) => {
    const at = a.last_used_at ? Date.parse(a.last_used_at) : 0;
    const bt = b.last_used_at ? Date.parse(b.last_used_at) : 0;
    if (at !== bt) return bt - at; // most recently used first
    return a.name.localeCompare(b.name);
  });
  return next.slice(0, BOOTSTRAP_GROUP_LIMIT);
};

export const useWorkspaceBootstrapStore = create<BootstrapState>()((set, get) => ({
  ...initialBootstrapState,

  load: async (force = false) => {
    // Fresh = loaded AND not marked stale (P1-4).
    if (!force && get().loadedAt && !get().dirty) return;
    if (inflight) return inflight;
    const myGeneration = generation;
    set({ isLoading: true, error: null });
    inflight = fetchWorkspaceBootstrap()
      .then((payload) => {
        // The identity underneath this request is gone: drop the answer
        // rather than writing another user's shortcuts (P0-2).
        if (generation !== myGeneration) return;
        set({
          defaultApplication: payload.default_application ?? null,
          favorites: payload.favorites ?? [],
          frequent: payload.frequent ?? [],
          recent: payload.recent ?? [],
          recommended: payload.recommended ?? [],
          recentFixedApps: payload.recent_fixed_apps ?? [],
          agentCategories: payload.agent_categories ?? [],
          appCategories: payload.app_categories ?? [],
          isLoading: false,
          loadedAt: Date.now(),
          dirty: false,
        });
      })
      .catch((error: any) => {
        if (generation !== myGeneration) return;
        set({
          error: error?.response?.data?.detail || '工作台数据加载失败',
          isLoading: false,
        });
      })
      .finally(() => { inflight = null; });
    return inflight;
  },

  invalidate: () => set({ dirty: true }),

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
    // 收藏 is a CHAT group; a fixed application only flips its own ✩ and is
    // never injected into the agent group (P1-5).
    const belongsToAgentFavorites = known.kind === 'chat';
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
        favorites: belongsToAgentFavorites
          ? applyFavorite(s.favorites, applicationId, favorite, known)
          : s.favorites,
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
      // Membership in 常用 / 最近 / 推荐 is a server-side Top-8 decision the
      // client cannot recompute, so mark stale and let the next home visit
      // settle it (P1-4).
      get().invalidate();
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

  clear: () => {
    // Bump FIRST: any request already in flight must see a stale generation
    // the moment it settles, not after the next statement.
    generation += 1;
    inflight = null;
    set({ ...initialBootstrapState });
  },
}));

// Every start-up fact here is per-user: the default agent, the four shortcut
// groups and both category rails. Forget them on a user switch (二次复审
// P0-2) — `clear()` also bumps the request generation, so an in-flight
// bootstrap for the PREVIOUS user cannot land afterwards.
registerSessionReset(() => useWorkspaceBootstrapStore.getState().clear());
