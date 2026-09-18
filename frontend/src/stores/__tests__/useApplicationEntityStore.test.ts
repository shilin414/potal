/**
 * useApplicationEntityStore (二次复审 P1-6, P0-2).
 *
 * P1-6: the store used to `.catch(() => undefined)` on EVERY rejection, so a
 * 500, a DB timeout and a dropped network all arrived at the caller as
 * `undefined` — the same value a real 404 produces. WorkspaceHost rendered
 * 「找不到应用：xxx」 for a backend outage, and HomeWorkspace deleted the
 * user's `?conversation=` deep link to "handle" what was really a transport
 * error. Now only a 404 is swallowed.
 *
 * P0-2: `clear()` bumps a generation so a resolve already in flight for the
 * previous user is dropped instead of being written into the new session.
 */

import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ resolve: vi.fn() }));

vi.mock('@/services/runApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/runApi')>();
  return { ...actual, resolveApplication: mocks.resolve };
});

import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { resetSessionScopedState } from '@/stores/resetSessionState';
import type { V2Application } from '@/services/runApi';

const app = (id: number, slug: string): V2Application => ({
  id, slug, name: `名称${id}`, description: '', icon: '', kind: 'chat',
  runtime_type: 'agent', provider_key: 'feishu_aily', capabilities: {},
});

/** An axios-shaped rejection carrying an HTTP status. */
const httpError = (status: number): unknown => {
  const error: any = new Error(`request failed with ${status}`);
  error.response = { status };
  return error;
};

beforeEach(() => {
  mocks.resolve.mockReset();
  resetSessionScopedState();
});

describe('entity store error semantics', () => {
  it('treats a 404 as "no such application"', async () => {
    mocks.resolve
      .mockRejectedValueOnce(httpError(404))
      .mockRejectedValueOnce(httpError(404));

    // Unknown and not-visible are deliberately the same answer: existence is
    // never leaked (执行报告 §12).
    await expect(useApplicationEntityStore.getState().ensure(1))
      .resolves.toBeUndefined();
    await expect(useApplicationEntityStore.getState().ensureBySlug('ghost'))
      .resolves.toBeUndefined();
  });

  it('does NOT swallow a 500 — the caller can tell it apart', async () => {
    mocks.resolve.mockRejectedValueOnce(httpError(500));

    await expect(useApplicationEntityStore.getState().ensure(2))
      .rejects.toBeTruthy();
  });

  it('does NOT swallow a timeout / network failure (no response at all)', async () => {
    mocks.resolve.mockRejectedValueOnce(new Error('Network Error'));

    await expect(useApplicationEntityStore.getState().ensureBySlug('sales'))
      .rejects.toBeTruthy();
  });

  it('caches a resolved row under BOTH id and slug', async () => {
    mocks.resolve.mockResolvedValueOnce(app(5, 'sales'));

    const resolved = await useApplicationEntityStore.getState().ensure(5);
    expect(resolved?.id).toBe(5);

    // Cache-first: the second lookup issues no request.
    await expect(useApplicationEntityStore.getState().ensureBySlug('sales'))
      .resolves.toMatchObject({ id: 5 });
    expect(mocks.resolve).toHaveBeenCalledTimes(1);
  });
});

describe('entity store session isolation', () => {
  it('drops a resolve that was in flight when the session ended', async () => {
    let release: (value: unknown) => void = () => {};
    mocks.resolve.mockReturnValueOnce(new Promise((done) => { release = done; }));

    const inflight = useApplicationEntityStore.getState().ensure(9);
    resetSessionScopedState();
    release(app(9, 'a-users-agent'));
    await inflight.catch(() => undefined);

    expect(useApplicationEntityStore.getState().byId[9]).toBeUndefined();
  });

  it('de-duplicates concurrent lookups for the same key', async () => {
    mocks.resolve.mockResolvedValue(app(11, 'dup'));

    const [a, b] = await Promise.all([
      useApplicationEntityStore.getState().ensure(11),
      useApplicationEntityStore.getState().ensure(11),
    ]);
    expect(a?.id).toBe(11);
    expect(b?.id).toBe(11);
    expect(mocks.resolve).toHaveBeenCalledTimes(1);
  });
});

describe('entity consume freshness (四次复审 P1-R1)', () => {
  it('revalidates a stale cached row before a consumer surface trusts it', async () => {
    mocks.resolve
      .mockResolvedValueOnce(app(21, 'sales'))
      .mockRejectedValueOnce(httpError(404));

    await expect(useApplicationEntityStore.getState().ensure(21))
      .resolves.toMatchObject({ id: 21 });
    // Cache hit within the TTL: no second request.
    await expect(useApplicationEntityStore.getState().ensure(21))
      .resolves.toMatchObject({ id: 21 });
    expect(mocks.resolve).toHaveBeenCalledTimes(1);

    // An entry point asks for maxAgeMs: 0 → revalidate. The server now says
    // 404 (disabled / provider killed): the stale row is removed.
    await expect(useApplicationEntityStore.getState().ensure(21, { maxAgeMs: 0 }))
      .resolves.toBeUndefined();
    expect(mocks.resolve).toHaveBeenCalledTimes(2);
    expect(useApplicationEntityStore.getState().byId[21]).toBeUndefined();
    expect(useApplicationEntityStore.getState().bySlug.sales).toBeUndefined();
  });

  it('hides a cached row when a revalidation reports a transport failure', async () => {
    mocks.resolve
      .mockResolvedValueOnce(app(22, 'ops'))
      .mockRejectedValueOnce(httpError(500));

    await useApplicationEntityStore.getState().ensure(22);
    await expect(useApplicationEntityStore.getState().ensure(22, { maxAgeMs: 0 }))
      .rejects.toBeTruthy();

    // 500 is not proof the app vanished, but it also is not proof it is
    // consumable: keep it out of consumer surfaces until a retry succeeds.
    expect(useApplicationEntityStore.getState().byId[22]).toBeUndefined();
  });
});
