/**
 * useApplicationEntityStore — the applications this session has actually
 * touched (执行报告 §10-B, §12, §13, P1-3).
 *
 * Why it exists: the shell needs the ENTITY behind a route, a favourite
 * button, "go back to the app you came from", or a conversation's owning
 * application. The old catalog mirror answered every one of those from a
 * single whole-catalog download. This store answers them from what was really
 * loaded:
 *
 *   · every application the user navigates INTO is resolved once
 *     (`GET /v2/applications/resolve?slug=` / `?id=`) and cached by id + slug;
 *   · every page browsed through `useApplicationPage` upserts its items;
 *   · a cache MISS triggers ONE single-application request — never a list.
 *
 * The rows here are full catalog items (the resolve endpoint answers in the
 * same shape the paged endpoint uses), so a cached entity can be handed to
 * any consumer that needs runtime fields (`capabilities`, `skills`,
 * `renderer_key`). The bootstrap's display-only summaries deliberately live
 * in `useWorkspaceBootstrapStore` instead — they are not entities.
 *
 * No TTL and no invalidation machinery: an application row is tiny, and every
 * write that changes one patches it here (see `patch` callers in AgentsPage),
 * which is exactly the report's "local patch, never a reload".
 */
import { create } from 'zustand';
import {
  resolveApplication,
  type V2Application,
} from '@/services/runApi';
import { registerSessionReset } from '@/stores/resetSessionState';

interface EntityState {
  byId: Record<number, V2Application>;
  bySlug: Record<string, V2Application>;

  upsert: (application: V2Application) => void;
  upsertMany: (applications: V2Application[]) => void;
  patch: (id: number, patch: Partial<V2Application>) => void;
  remove: (id: number) => void;
  clear: () => void;

  get: (id: number | null | undefined) => V2Application | undefined;
  getBySlug: (slug: string | undefined) => V2Application | undefined;

  /**
   * Resolve one application, cache-first.
   *
   * Resolves to `undefined` ONLY for a genuine 404 — "unknown or not visible
   * to this caller", which the API answers identically on purpose
   * (existence is never leaked).
   *
   * Every OTHER failure (500, timeout, network down) is REJECTED (二次复审
   * P1-6). Swallowing all of them made a backend outage look exactly like
   * "找不到应用：xxx", and HomeWorkspace then deleted the user's
   * `?conversation=` deep link — destroying real navigation state to hide a
   * transport error.
   */
  ensure: (id: number | null | undefined) => Promise<V2Application | undefined>;
  /** Same, by slug — the /chat/:slug and /app/:slug deep-link path. */
  ensureBySlug: (slug: string | undefined) => Promise<V2Application | undefined>;
}

/** De-duplicates concurrent lookups for the SAME key (deep-link remounts). */
const inflight = new Map<string, Promise<V2Application | undefined>>();

/**
 * Bumped by `clear()` (二次复审 P0-2 §6). A resolve that was already in
 * flight when the session ended must not write its answer afterwards: its
 * generation no longer matches and its result is dropped. Clearing the map
 * alone is not enough — the promise is still running and still resolves.
 */
let generation = 0;

/** True only for a real 404 — the one failure that means "no such row". */
function isNotFound(error: unknown): boolean {
  const status = (error as any)?.response?.status;
  return status === 404;
}

export const useApplicationEntityStore = create<EntityState>()((set, get) => ({
  byId: {},
  bySlug: {},

  upsert: (application) => set((state) => ({
    byId: { ...state.byId, [application.id]: application },
    bySlug: { ...state.bySlug, [application.slug]: application },
  })),

  upsertMany: (applications) => {
    if (!applications.length) return;
    set((state) => {
      const byId = { ...state.byId };
      const bySlug = { ...state.bySlug };
      for (const application of applications) {
        byId[application.id] = application;
        bySlug[application.slug] = application;
      }
      return { byId, bySlug };
    });
  },

  patch: (id, patch) => set((state) => {
    const current = state.byId[id];
    if (!current) return {};
    const next = { ...current, ...patch };
    const bySlug = { ...state.bySlug };
    // A slug rename is not a supported edit (the API keeps slugs stable), but
    // re-keying defensively is cheaper than leaving a stale slug entry that
    // would make a later deep link resolve to the OLD row.
    if (next.slug !== current.slug) delete bySlug[current.slug];
    bySlug[next.slug] = next;
    return { byId: { ...state.byId, [id]: next }, bySlug };
  }),

  remove: (id) => set((state) => {
    const hit = state.byId[id];
    const byId = { ...state.byId };
    delete byId[id];
    const bySlug = { ...state.bySlug };
    if (hit) delete bySlug[hit.slug];
    return { byId, bySlug };
  }),

  clear: () => {
    generation += 1;
    inflight.clear();
    set({ byId: {}, bySlug: {} });
  },

  get: (id) => (id == null ? undefined : get().byId[id]),
  getBySlug: (slug) => (slug ? get().bySlug[slug] : undefined),

  ensure: (id) => {
    if (id == null) return Promise.resolve(undefined);
    const cached = get().byId[id];
    if (cached) return Promise.resolve(cached);
    const key = `id:${id}`;
    const pending = inflight.get(key);
    if (pending) return pending;
    const myGeneration = generation;
    const request = resolveApplication({ id })
      .then((application) => {
        // The session this request belongs to is over (P0-2).
        if (generation !== myGeneration) return undefined;
        get().upsert(application);
        return application;
      })
      .catch((error: unknown) => {
        if (generation !== myGeneration) return undefined;
        if (isNotFound(error)) return undefined;
        throw error;
      })
      .finally(() => {
        if (inflight.get(key) === request) inflight.delete(key);
      });
    inflight.set(key, request);
    return request;
  },

  ensureBySlug: (slug) => {
    if (!slug) return Promise.resolve(undefined);
    const cached = get().bySlug[slug];
    if (cached) return Promise.resolve(cached);
    const key = `slug:${slug}`;
    const pending = inflight.get(key);
    if (pending) return pending;
    const myGeneration = generation;
    const request = resolveApplication({ slug })
      .then((application) => {
        if (generation !== myGeneration) return undefined;
        get().upsert(application);
        return application;
      })
      .catch((error: unknown) => {
        if (generation !== myGeneration) return undefined;
        if (isNotFound(error)) return undefined;
        throw error;
      })
      .finally(() => {
        if (inflight.get(key) === request) inflight.delete(key);
      });
    inflight.set(key, request);
    return request;
  },
}));

// Resolved application rows are cached by id + slug; they are per-caller
// (visibility differs) and must not survive a user switch (P0-2). `clear()`
// bumps the generation so an in-flight resolve is dropped, not written.
registerSessionReset(() => useApplicationEntityStore.getState().clear());
