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
 * Consume freshness (四次复审 P1-R1): a cached entity also records WHEN it
 * was last returned by a consume-eligible endpoint (`/resolve`, a consume
 * page, bootstrap). A row whose consume validation is older than
 * `DEFAULT_CONSUME_TTL_MS` is re-resolved before a consumer surface trusts
 * it, so an admin disabling an application / provider / binding cannot be
 * masked by an arbitrarily old cache. The authoritative security boundary is
 * still Run admission; this only keeps the UI from offering something the
 * next Send would refuse.
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
  /**
   * application id → epoch ms of the last consume-eligible validation.
   * Absent means "never validated for consumption" (e.g. a manage-mode row
   * or a local patch), which forces the next consumer lookup to revalidate.
   */
  validatedAtById: Record<number, number>;

  upsert: (application: V2Application) => void;
  upsertMany: (applications: V2Application[]) => void;
  /**
   * Upsert rows returned by a CONSUME-eligible source and stamp them as
   * freshly validated (四次复审 P1-R1): `mode=consume` pages, `/resolve` and
   * the workspace bootstrap's rows (the latter are summaries, so only rows
   * already cached as entities gain the stamp).
   */
  markConsumeValidated: (applications: Array<Pick<V2Application, 'id'>>) => void;
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
  ensure: (
    id: number | null | undefined,
    options?: EntityResolveOptions,
  ) => Promise<V2Application | undefined>;
  /** Same, by slug — the /chat/:slug and /app/:slug deep-link path. */
  ensureBySlug: (
    slug: string | undefined,
    options?: EntityResolveOptions,
  ) => Promise<V2Application | undefined>;
}

export interface EntityResolveOptions {
  /** Force a network revalidation even when the cached row is fresh. */
  fresh?: boolean;
  /** Maximum accepted age of the last consume validation (ms). */
  maxAgeMs?: number;
}

/**
 * Consumer surfaces revalidate at most once per 45 seconds by default.
 * Opening a route / clicking 最近使用 passes `maxAgeMs: 0`, so every entry
 * revalidates immediately instead of trusting a cache that may predate an
 * admin's kill switch.
 */
export const DEFAULT_CONSUME_TTL_MS = 45_000;

/**
 * A failed revalidation must not hammer /resolve on every render. The row is
 * hidden while the backoff lasts, so the UI cannot present it as runnable.
 */
const REVALIDATION_RETRY_BACKOFF_MS = 5_000;

/** De-duplicates concurrent lookups for the SAME key (deep-link remounts). */
const inflight = new Map<string, Promise<V2Application | undefined>>();
const revalidateAfterById = new Map<number, number>();

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

/**
 * A cached row is consumer-trustworthy only when it was validated recently
 * enough for the requested surface.
 */
function isFresh(
  state: Pick<EntityState, 'validatedAtById'>,
  id: number,
  maxAgeMs: number,
): boolean {
  // maxAgeMs: 0 means "always revalidate" — comparing timestamps would
  // otherwise accept a validation that happened in the same millisecond.
  if (maxAgeMs <= 0) return false;
  const validatedAt = state.validatedAtById[id];
  if (validatedAt == null) return false;
  return Date.now() - validatedAt <= maxAgeMs;
}

function isRetrying(id: number): boolean {
  const until = revalidateAfterById.get(id);
  return until != null && until > Date.now();
}

function markValidation(id: number, at = Date.now()): void {
  revalidateAfterById.delete(id);
  const current = useApplicationEntityStore.getState().validatedAtById[id];
  // A row can be upserted twice for one resolve (page + route); never let a
  // stale write move the validation timestamp backwards.
  if (current != null && current > at) return;
  useApplicationEntityStore.setState((state) => ({
    validatedAtById: { ...state.validatedAtById, [id]: at },
  }));
}

function hideUntilRetry(id: number): void {
  revalidateAfterById.set(id, Date.now() + REVALIDATION_RETRY_BACKOFF_MS);
  const { byId, bySlug } = useApplicationEntityStore.getState();
  const hit = byId[id];
  if (!hit) return;
  const nextById = { ...byId };
  delete nextById[id];
  const nextBySlug = { ...bySlug };
  delete nextBySlug[hit.slug];
  useApplicationEntityStore.setState({ byId: nextById, bySlug: nextBySlug });
}

function forget(id: number): void {
  revalidateAfterById.delete(id);
  const { byId, bySlug, validatedAtById } = useApplicationEntityStore.getState();
  const hit = byId[id];
  const nextById = { ...byId };
  delete nextById[id];
  const nextBySlug = { ...bySlug };
  if (hit) delete nextBySlug[hit.slug];
  const nextValidated = { ...validatedAtById };
  delete nextValidated[id];
  useApplicationEntityStore.setState({
    byId: nextById,
    bySlug: nextBySlug,
    validatedAtById: nextValidated,
  });
}

export const useApplicationEntityStore = create<EntityState>()((set, get) => ({
  byId: {},
  bySlug: {},
  validatedAtById: {},

  upsert: (application) => set((state) => ({
    byId: { ...state.byId, [application.id]: application },
    bySlug: { ...state.bySlug, [application.slug]: application },
    // A plain upsert only records DATA; it is not a consume validation.
    // The resolve paths stamp freshness explicitly through markValidation.
    validatedAtById: state.validatedAtById,
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

  markConsumeValidated: (applications) => {
    if (!applications.length) return;
    const now = Date.now();
    set((state) => {
      const validatedAtById = { ...state.validatedAtById };
      for (const application of applications) {
        // Only rows already known as entities can be stamped: a summary has
        // no runtime facts, and stamping its id would make a later `get`
        // claim a validation the cache never actually held.
        if (!state.byId[application.id]) continue;
        validatedAtById[application.id] = now;
        revalidateAfterById.delete(application.id);
      }
      return { validatedAtById };
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
    revalidateAfterById.clear();
    set({ byId: {}, bySlug: {}, validatedAtById: {} });
  },

  get: (id) => (id == null ? undefined : get().byId[id]),
  getBySlug: (slug) => (slug ? get().bySlug[slug] : undefined),

  ensure: (id, options) => {
    if (id == null) return Promise.resolve(undefined);
    const cached = get().byId[id];
    const maxAgeMs = options?.maxAgeMs ?? DEFAULT_CONSUME_TTL_MS;
    if (!options?.fresh && cached && isFresh(get(), id, maxAgeMs)) {
      return Promise.resolve(cached);
    }
    if (isRetrying(id)) return Promise.resolve(undefined);
    const key = `id:${id}`;
    const pending = inflight.get(key);
    if (pending) return pending;
    const myGeneration = generation;
    const request = resolveApplication({ id })
      .then((application) => {
        // The session this request belongs to is over (P0-2).
        if (generation !== myGeneration) return undefined;
        get().upsert(application);
        markValidation(application.id);
        return application;
      })
      .catch((error: unknown) => {
        if (generation !== myGeneration) return undefined;
        if (isNotFound(error)) {
          forget(id);
          return undefined;
        }
        // 500 / timeout / offline: never pretend the old row is runnable.
        // Keep it out of consumer surfaces until the next successful retry.
        hideUntilRetry(id);
        throw error;
      })
      .finally(() => {
        if (inflight.get(key) === request) inflight.delete(key);
      });
    inflight.set(key, request);
    return request;
  },

  ensureBySlug: (slug, options) => {
    if (!slug) return Promise.resolve(undefined);
    const cached = get().bySlug[slug];
    const maxAgeMs = options?.maxAgeMs ?? DEFAULT_CONSUME_TTL_MS;
    if (
      !options?.fresh
      && cached
      && isFresh(get(), cached.id, maxAgeMs)
    ) {
      return Promise.resolve(cached);
    }
    if (cached && isRetrying(cached.id)) return Promise.resolve(undefined);
    const key = `slug:${slug}`;
    const pending = inflight.get(key);
    if (pending) return pending;
    const myGeneration = generation;
    const request = resolveApplication({ slug })
      .then((application) => {
        if (generation !== myGeneration) return undefined;
        get().upsert(application);
        markValidation(application.id);
        return application;
      })
      .catch((error: unknown) => {
        if (generation !== myGeneration) return undefined;
        if (isNotFound(error)) {
          if (cached) forget(cached.id);
          return undefined;
        }
        if (cached) hideUntilRetry(cached.id);
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
