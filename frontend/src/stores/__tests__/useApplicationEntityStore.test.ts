import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ resolve: vi.fn() }));

vi.mock('@/services/runApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/runApi')>();
  return { ...actual, resolveApplication: mocks.resolve };
});

import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { resetSessionScopedState } from '@/stores/resetSessionState';
import type { V2Application } from '@/services/runApi';

const app = (id: number, slug: string, name = `名称${id}`): V2Application => ({
  id, slug, name, description: '', icon: '', kind: 'chat',
  runtime_type: 'agent', provider_key: 'feishu_aily', capabilities: {},
});

const httpError = (status: number): unknown => {
  const error: any = new Error(`request failed with ${status}`);
  error.response = { status };
  return error;
};

beforeEach(() => {
  mocks.resolve.mockReset();
  resetSessionScopedState();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('entity store error semantics', () => {
  it('returns undefined only for an authoritative 404', async () => {
    mocks.resolve
      .mockRejectedValueOnce(httpError(404))
      .mockRejectedValueOnce(httpError(404));

    await expect(useApplicationEntityStore.getState().ensure(1))
      .resolves.toBeUndefined();
    await expect(useApplicationEntityStore.getState().ensureBySlug('ghost'))
      .resolves.toBeUndefined();
  });

  it('does not swallow a 500 or network failure', async () => {
    mocks.resolve
      .mockRejectedValueOnce(httpError(500))
      .mockRejectedValueOnce(new Error('Network Error'));

    await expect(useApplicationEntityStore.getState().ensure(2)).rejects.toBeTruthy();
    await expect(useApplicationEntityStore.getState().ensureBySlug('sales'))
      .rejects.toBeTruthy();
  });

  it('caches a consume response under id and slug with one trust stamp', async () => {
    mocks.resolve.mockResolvedValueOnce(app(5, 'sales'));

    await expect(useApplicationEntityStore.getState().ensure(5))
      .resolves.toMatchObject({ id: 5 });
    await expect(useApplicationEntityStore.getState().ensureBySlug('sales'))
      .resolves.toMatchObject({ id: 5 });
    expect(mocks.resolve).toHaveBeenCalledTimes(1);
    expect(useApplicationEntityStore.getState().validatedAtById[5]).toEqual(expect.any(Number));
  });
});

describe('entity store session isolation', () => {
  it('drops a resolve that was in flight when the session ended', async () => {
    let release: (value: unknown) => void = () => {};
    mocks.resolve.mockReturnValueOnce(new Promise((done) => { release = done; }));

    const pending = useApplicationEntityStore.getState().ensure(9);
    resetSessionScopedState();
    release(app(9, 'a-users-agent'));
    await pending.catch(() => undefined);

    expect(useApplicationEntityStore.getState().byId[9]).toBeUndefined();
  });

  it('de-duplicates concurrent lookups for the same key', async () => {
    mocks.resolve.mockResolvedValue(app(11, 'dup'));

    const [first, second] = await Promise.all([
      useApplicationEntityStore.getState().ensure(11),
      useApplicationEntityStore.getState().ensure(11),
    ]);
    expect(first?.id).toBe(11);
    expect(second?.id).toBe(11);
    expect(mocks.resolve).toHaveBeenCalledTimes(1);
  });
});

describe('entity data and consume trust', () => {
  it('invalidates consume trust when management data overwrites an entity', async () => {
    mocks.resolve
      .mockResolvedValueOnce(app(21, 'sales', 'consume-old'))
      .mockResolvedValueOnce(app(21, 'sales', 'consume-new'));

    await useApplicationEntityStore.getState().ensure(21);
    useApplicationEntityStore.getState().upsertManage(app(21, 'sales', 'manage-new'));
    expect(useApplicationEntityStore.getState().validatedAtById[21]).toBeUndefined();

    await expect(useApplicationEntityStore.getState().ensure(21))
      .resolves.toMatchObject({ name: 'consume-new' });
    expect(mocks.resolve).toHaveBeenCalledTimes(2);
  });

  it('atomically replaces old data and stamps a consume page response', async () => {
    useApplicationEntityStore.getState().upsertManage(app(22, 'ops', 'old'));
    useApplicationEntityStore.getState().upsertManyConsume([app(22, 'ops', 'new')]);

    await expect(useApplicationEntityStore.getState().ensure(22))
      .resolves.toMatchObject({ name: 'new' });
    expect(mocks.resolve).not.toHaveBeenCalled();
    expect(useApplicationEntityStore.getState().validatedAtById[22]).toEqual(expect.any(Number));
  });

  it('patch is a management write and invalidates an existing trust stamp', async () => {
    mocks.resolve.mockResolvedValueOnce(app(23, 'patched'));
    await useApplicationEntityStore.getState().ensure(23);

    useApplicationEntityStore.getState().patch(23, { enabled: false });

    expect(useApplicationEntityStore.getState().byId[23]?.enabled).toBe(false);
    expect(useApplicationEntityStore.getState().validatedAtById[23]).toBeUndefined();
  });

  it('removes both data and trust after a failed forced revalidation', async () => {
    mocks.resolve
      .mockResolvedValueOnce(app(24, 'billing'))
      .mockRejectedValueOnce(httpError(500));

    await useApplicationEntityStore.getState().ensure(24);
    await expect(useApplicationEntityStore.getState().ensure(24, { maxAgeMs: 0 }))
      .rejects.toBeTruthy();

    const state = useApplicationEntityStore.getState();
    expect(state.byId[24]).toBeUndefined();
    expect(state.bySlug.billing).toBeUndefined();
    expect(state.validatedAtById[24]).toBeUndefined();
  });

  it('throttles a failed slug lookup even after the entity is hidden', async () => {
    let now = 1_000_000;
    vi.spyOn(Date, 'now').mockImplementation(() => now);
    mocks.resolve
      .mockResolvedValueOnce(app(25, 'sales'))
      .mockRejectedValueOnce(httpError(500))
      .mockResolvedValueOnce(app(25, 'sales', 'recovered'));

    await useApplicationEntityStore.getState().ensureBySlug('sales');
    await expect(useApplicationEntityStore.getState().ensureBySlug('sales', { maxAgeMs: 0 }))
      .rejects.toBeTruthy();
    await expect(useApplicationEntityStore.getState().ensureBySlug('sales'))
      .rejects.toThrow('deferred');
    expect(mocks.resolve).toHaveBeenCalledTimes(2);

    now += 5_001;
    await expect(useApplicationEntityStore.getState().ensureBySlug('sales'))
      .resolves.toMatchObject({ name: 'recovered' });
    expect(mocks.resolve).toHaveBeenCalledTimes(3);
  });
});

  it('lets an explicit user retry bypass the automatic failure backoff', async () => {
    mocks.resolve
      .mockRejectedValueOnce(httpError(500))
      .mockResolvedValueOnce(app(26, 'manual-retry', 'recovered'));

    await expect(useApplicationEntityStore.getState().ensureBySlug('manual-retry'))
      .rejects.toBeTruthy();
    await expect(useApplicationEntityStore.getState().ensureBySlug('manual-retry'))
      .rejects.toThrow('deferred');
    await expect(useApplicationEntityStore.getState().ensureBySlug('manual-retry', {
      maxAgeMs: 0,
      bypassBackoff: true,
    })).resolves.toMatchObject({ name: 'recovered' });

    expect(mocks.resolve).toHaveBeenCalledTimes(2);
  });
