/**
 * Session-scoped cache of full application entities.
 *
 * Entity data and consume trust are deliberately separate. Management reads
 * may populate the cache, but only a complete response from a consume endpoint
 * may stamp an entity as safe to reuse without another `/resolve` call.
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
  /** application id → epoch ms of the complete consume response cached here. */
  validatedAtById: Record<number, number>;

  /** Management data updates the entity and invalidates any older consume trust. */
  upsertManage: (application: V2Application) => void;
  upsertManyManage: (applications: V2Application[]) => void;
  /** Consume data atomically updates both the entity snapshot and its trust stamp. */
  upsertConsume: (application: V2Application) => void;
  upsertManyConsume: (applications: V2Application[]) => void;
  /** Local/authoring mutations are management writes and therefore invalidate trust. */
  patch: (id: number, patch: Partial<V2Application>) => void;
  remove: (id: number) => void;
  clear: () => void;

  get: (id: number | null | undefined) => V2Application | undefined;
  getBySlug: (slug: string | undefined) => V2Application | undefined;
  ensure: (
    id: number | null | undefined,
    options?: EntityResolveOptions,
  ) => Promise<V2Application | undefined>;
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
  /** Manual user retry: bypass the automatic failure backoff once. */
  bypassBackoff?: boolean;
}

export const DEFAULT_CONSUME_TTL_MS = 45_000;
const REVALIDATION_RETRY_BACKOFF_MS = 5_000;

/** De-duplicates concurrent lookups for the same route/entity key. */
const inflight = new Map<string, Promise<V2Application | undefined>>();
/** Retry throttling survives removal of the failed entity because it is keyed by lookup. */
const retryAfterByLookup = new Map<string, number>();
let generation = 0;

function idKey(id: number): string {
  return `id:${id}`;
}

function slugKey(slug: string): string {
  return `slug:${slug}`;
}

function isNotFound(error: unknown): boolean {
  return (error as any)?.response?.status === 404;
}

function isFresh(
  state: Pick<EntityState, 'validatedAtById'>,
  id: number,
  maxAgeMs: number,
): boolean {
  if (maxAgeMs <= 0) return false;
  const validatedAt = state.validatedAtById[id];
  return validatedAt != null && Date.now() - validatedAt <= maxAgeMs;
}

function retryError(key: string): Error | null {
  const until = retryAfterByLookup.get(key);
  if (until == null) return null;
  if (until <= Date.now()) {
    retryAfterByLookup.delete(key);
    return null;
  }
  return new Error(`application revalidation is deferred until ${until}`);
}

function clearRetryFor(application: Pick<V2Application, 'id' | 'slug'>): void {
  retryAfterByLookup.delete(idKey(application.id));
  retryAfterByLookup.delete(slugKey(application.slug));
}

function stampRetryFor(
  failedLookup: string,
  application?: Pick<V2Application, 'id' | 'slug'>,
): void {
  const until = Date.now() + REVALIDATION_RETRY_BACKOFF_MS;
  retryAfterByLookup.set(failedLookup, until);
  if (application) {
    retryAfterByLookup.set(idKey(application.id), until);
    retryAfterByLookup.set(slugKey(application.slug), until);
  }
}

function mergeEntities(
  state: Pick<EntityState, 'byId' | 'bySlug' | 'validatedAtById'>,
  applications: V2Application[],
  source: 'manage' | 'consume',
): Pick<EntityState, 'byId' | 'bySlug' | 'validatedAtById'> {
  const byId = { ...state.byId };
  const bySlug = { ...state.bySlug };
  const validatedAtById = { ...state.validatedAtById };
  const validatedAt = Date.now();

  for (const application of applications) {
    const previousByID = byId[application.id];
    if (previousByID && previousByID.slug !== application.slug) {
      delete bySlug[previousByID.slug];
    }
    const previousBySlug = bySlug[application.slug];
    if (previousBySlug && previousBySlug.id !== application.id) {
      delete byId[previousBySlug.id];
      delete validatedAtById[previousBySlug.id];
      clearRetryFor(previousBySlug);
    }

    byId[application.id] = application;
    bySlug[application.slug] = application;
    if (source === 'consume') {
      validatedAtById[application.id] = validatedAt;
    } else {
      delete validatedAtById[application.id];
    }
    clearRetryFor(application);
  }

  return { byId, bySlug, validatedAtById };
}

function removeEntity(
  state: Pick<EntityState, 'byId' | 'bySlug' | 'validatedAtById'>,
  id: number,
): Pick<EntityState, 'byId' | 'bySlug' | 'validatedAtById'> {
  const hit = state.byId[id];
  const byId = { ...state.byId };
  delete byId[id];
  const bySlug = { ...state.bySlug };
  if (hit) delete bySlug[hit.slug];
  const validatedAtById = { ...state.validatedAtById };
  delete validatedAtById[id];
  return { byId, bySlug, validatedAtById };
}

function hideUntilRetry(
  id: number,
  failedLookup: string,
  fallback?: Pick<V2Application, 'id' | 'slug'>,
): void {
  const current = useApplicationEntityStore.getState().byId[id] ?? fallback;
  stampRetryFor(failedLookup, current);
  useApplicationEntityStore.setState((state) => removeEntity(state, id));
}

function forget(id: number, lookupKey?: string): void {
  const current = useApplicationEntityStore.getState().byId[id];
  if (current) clearRetryFor(current);
  retryAfterByLookup.delete(idKey(id));
  if (lookupKey) retryAfterByLookup.delete(lookupKey);
  useApplicationEntityStore.setState((state) => removeEntity(state, id));
}

export const useApplicationEntityStore = create<EntityState>()((set, get) => ({
  byId: {},
  bySlug: {},
  validatedAtById: {},

  upsertManage: (application) => set((state) => (
    mergeEntities(state, [application], 'manage')
  )),

  upsertManyManage: (applications) => {
    if (!applications.length) return;
    set((state) => mergeEntities(state, applications, 'manage'));
  },

  upsertConsume: (application) => set((state) => (
    mergeEntities(state, [application], 'consume')
  )),

  upsertManyConsume: (applications) => {
    if (!applications.length) return;
    set((state) => mergeEntities(state, applications, 'consume'));
  },

  patch: (id, patch) => set((state) => {
    const current = state.byId[id];
    if (!current) return {};
    const next = { ...current, ...patch };
    clearRetryFor(current);
    clearRetryFor(next);
    return mergeEntities(state, [next], 'manage');
  }),

  remove: (id) => set((state) => {
    const current = state.byId[id];
    if (current) clearRetryFor(current);
    retryAfterByLookup.delete(idKey(id));
    return removeEntity(state, id);
  }),

  clear: () => {
    generation += 1;
    inflight.clear();
    retryAfterByLookup.clear();
    set({ byId: {}, bySlug: {}, validatedAtById: {} });
  },

  get: (id) => (id == null ? undefined : get().byId[id]),
  getBySlug: (slug) => (slug ? get().bySlug[slug] : undefined),

  ensure: (id, options) => {
    if (id == null) return Promise.resolve(undefined);
    const key = idKey(id);
    if (!options?.bypassBackoff) {
      const deferred = retryError(key);
      if (deferred) return Promise.reject(deferred);
    }

    const cached = get().byId[id];
    const maxAgeMs = options?.maxAgeMs ?? DEFAULT_CONSUME_TTL_MS;
    if (!options?.fresh && cached && isFresh(get(), id, maxAgeMs)) {
      return Promise.resolve(cached);
    }
    const pending = inflight.get(key);
    if (pending) return pending;

    const myGeneration = generation;
    const request = resolveApplication({ id })
      .then((application) => {
        if (generation !== myGeneration) return undefined;
        get().upsertConsume(application);
        return application;
      })
      .catch((error: unknown) => {
        if (generation !== myGeneration) return undefined;
        if (isNotFound(error)) {
          forget(id, key);
          return undefined;
        }
        hideUntilRetry(id, key, cached);
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
    const key = slugKey(slug);
    if (!options?.bypassBackoff) {
      const deferred = retryError(key);
      if (deferred) return Promise.reject(deferred);
    }

    const cached = get().bySlug[slug];
    const maxAgeMs = options?.maxAgeMs ?? DEFAULT_CONSUME_TTL_MS;
    if (
      !options?.fresh
      && cached
      && isFresh(get(), cached.id, maxAgeMs)
    ) {
      return Promise.resolve(cached);
    }
    const pending = inflight.get(key);
    if (pending) return pending;

    const myGeneration = generation;
    const request = resolveApplication({ slug })
      .then((application) => {
        if (generation !== myGeneration) return undefined;
        get().upsertConsume(application);
        return application;
      })
      .catch((error: unknown) => {
        if (generation !== myGeneration) return undefined;
        if (isNotFound(error)) {
          if (cached) forget(cached.id, key);
          else retryAfterByLookup.delete(key);
          return undefined;
        }
        if (cached) {
          hideUntilRetry(cached.id, key, cached);
        } else {
          stampRetryFor(key);
        }
        throw error;
      })
      .finally(() => {
        if (inflight.get(key) === request) inflight.delete(key);
      });
    inflight.set(key, request);
    return request;
  },
}));

registerSessionReset(() => useApplicationEntityStore.getState().clear());
