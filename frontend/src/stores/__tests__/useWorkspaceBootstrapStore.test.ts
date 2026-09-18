/**
 * useWorkspaceBootstrapStore — the shell's start-up facts (执行报告 §9–§11).
 *
 * It replaces `useApplicationCatalogStore`, so the behaviours the old store
 * was tested for still have to hold — but now they are enforced against a
 * CONSTANT-SIZE payload instead of a whole catalog:
 *
 *   · `load()` is once-only unless forced, and concurrent callers share one
 *     request (the shell mounts several consumers);
 *   · a favourite toggle updates every group the row appears in, optimistically,
 *     and reverts exactly when the server rejects it;
 *   · a delete drops the row everywhere (including the default agent);
 *   · `summaryById` / `summaryBySlug` are the lookup fallback for rows that
 *     are in this payload.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  fetchWorkspaceBootstrap: vi.fn(),
  setApplicationFavorite: vi.fn(),
  resolveApplication: vi.fn(),
}));

vi.mock('@/services/runApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/services/runApi')>()),
  fetchWorkspaceBootstrap: mocks.fetchWorkspaceBootstrap,
  setApplicationFavorite: mocks.setApplicationFavorite,
  resolveApplication: mocks.resolveApplication,
}));

import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import type { ApplicationSummary, WorkspaceBootstrap } from '@/services/runApi';

const summary = (over: Partial<ApplicationSummary> & { id: number; name: string }): ApplicationSummary => ({
  slug: `slug-${over.id}`, description: '', icon: '', kind: 'chat', enabled: true,
  ...over,
});

const DEFAULT_AGENT = summary({ id: 1, name: '问数小安', is_bound: true, is_default_agent: true });

const payload = (over: Partial<WorkspaceBootstrap> = {}): WorkspaceBootstrap => ({
  default_application: DEFAULT_AGENT,
  favorites: [],
  frequent: [summary({ id: 2, name: '运维小安', usage_count: 3 })],
  recent: [summary({ id: 3, name: '合同助手' })],
  recommended: [],
  recent_fixed_apps: [summary({ id: 10, name: 'OA密码修改', kind: 'custom' })],
  agent_categories: [{ slug: 'it', name: 'IT运维', count: 2 }],
  app_categories: [{ slug: 'office', name: '办公', count: 1 }],
  ...over,
});

beforeEach(() => {
  mocks.fetchWorkspaceBootstrap.mockReset().mockResolvedValue(payload());
  mocks.setApplicationFavorite.mockReset().mockResolvedValue({
    application_id: 2, is_favorite: true,
  });
  mocks.resolveApplication.mockReset();
  useWorkspaceBootstrapStore.getState().clear();
  useApplicationEntityStore.getState().clear();
});

describe('load', () => {
  it('stores the groups, the main agent and the category rails', async () => {
    await useWorkspaceBootstrapStore.getState().load();

    const state = useWorkspaceBootstrapStore.getState();
    expect(state.defaultApplication?.id).toBe(1);
    expect(state.frequent.map((a) => a.id)).toEqual([2]);
    expect(state.recent.map((a) => a.id)).toEqual([3]);
    expect(state.recentFixedApps.map((a) => a.id)).toEqual([10]);
    expect(state.agentCategories).toEqual([{ slug: 'it', name: 'IT运维', count: 2 }]);
    expect(state.appCategories).toEqual([{ slug: 'office', name: '办公', count: 1 }]);
  });

  it('does not let summary-only bootstrap data freshen a full entity', async () => {
    useApplicationEntityStore.getState().upsertManage({
      id: 2, slug: 'slug-2', name: '旧实体', description: '', icon: '', kind: 'chat',
      runtime_type: 'agent', provider_key: 'feishu_aily', capabilities: {},
    });
    mocks.resolveApplication.mockResolvedValueOnce({
      id: 2, slug: 'slug-2', name: 'resolve 新实体', description: '', icon: '', kind: 'chat',
      runtime_type: 'agent', provider_key: 'feishu_aily', capabilities: {},
    });

    await useWorkspaceBootstrapStore.getState().load();
    await expect(useApplicationEntityStore.getState().ensure(2))
      .resolves.toMatchObject({ name: 'resolve 新实体' });

    expect(mocks.resolveApplication).toHaveBeenCalledTimes(1);
  });

  it('fetches once, and shares one request between concurrent callers', async () => {
    await Promise.all([
      useWorkspaceBootstrapStore.getState().load(),
      useWorkspaceBootstrapStore.getState().load(),
    ]);
    expect(mocks.fetchWorkspaceBootstrap).toHaveBeenCalledTimes(1);

    await useWorkspaceBootstrapStore.getState().load();
    expect(mocks.fetchWorkspaceBootstrap).toHaveBeenCalledTimes(1);

    await useWorkspaceBootstrapStore.getState().load(true);
    expect(mocks.fetchWorkspaceBootstrap).toHaveBeenCalledTimes(2);
  });

  it('keeps the shell usable when the payload cannot be fetched', async () => {
    mocks.fetchWorkspaceBootstrap.mockRejectedValue({
      response: { data: { detail: 'boom' } },
    });

    await useWorkspaceBootstrapStore.getState().load();

    const state = useWorkspaceBootstrapStore.getState();
    expect(state.error).toBe('boom');
    expect(state.isLoading).toBe(false);
    expect(state.loadedAt).toBe(0); // a failure must not look like a load
  });

  // ── bootstrap races (三次复审 P1-R2) — deterministic promise control ──

  it('keeps dirty when a response that started BEFORE invalidate() lands', async () => {
    let release: (value: WorkspaceBootstrap) => void = () => {};
    mocks.fetchWorkspaceBootstrap.mockReturnValueOnce(
      new Promise((done) => { release = done; }));

    const inflight = useWorkspaceBootstrapStore.getState().load();
    // A mutation fires while the request is travelling.
    useWorkspaceBootstrapStore.getState().invalidate();
    release(payload());
    await inflight;

    // The OLD code reset dirty:false here — branding a pre-mutation
    // snapshot as fresh forever.
    const state = useWorkspaceBootstrapStore.getState();
    expect(state.dirty).toBe(true);
    expect(state.loadedAt).toBeGreaterThan(0);
  });

  it('does not let a previous session\u2019s settled request break the new session\u2019s dedup', async () => {
    let releaseA: (value: WorkspaceBootstrap) => void = () => {};
    mocks.fetchWorkspaceBootstrap
      .mockReturnValueOnce(new Promise<WorkspaceBootstrap>((done) => { releaseA = done; }))
      .mockReturnValueOnce(new Promise<WorkspaceBootstrap>(() => {})); // B stays pending

    const inflightA = useWorkspaceBootstrapStore.getState().load();
    // A → B switch while A is still travelling.
    useWorkspaceBootstrapStore.getState().clear();
    void useWorkspaceBootstrapStore.getState().load(); // B's request starts here

    releaseA(payload());
    await inflightA.catch(() => undefined);

    // A's `.finally` must not null B's in-flight pointer: a concurrent
    // load() must still SHARE B's request — no third fetch may start
    // (load() is async, so promises are wrapped per call; the observable
    // identity is the request count).
    void useWorkspaceBootstrapStore.getState().load();
    expect(mocks.fetchWorkspaceBootstrap).toHaveBeenCalledTimes(2);

    useWorkspaceBootstrapStore.getState().clear();
  });

  it('force reload drains an in-flight request and returns post-mutation data', async () => {
    let releaseFirst: (value: WorkspaceBootstrap) => void = () => {};
    mocks.fetchWorkspaceBootstrap
      .mockReturnValueOnce(new Promise<WorkspaceBootstrap>((done) => { releaseFirst = done; }))
      .mockResolvedValueOnce(payload({ frequent: [summary({ id: 9, name: '变更后的常用', usage_count: 9 })] }));

    const first = useWorkspaceBootstrapStore.getState().load();
    // force=true arrives while the first request is still travelling: it
    // must NOT return that stale promise.
    const forced = useWorkspaceBootstrapStore.getState().load(true);
    releaseFirst(payload());
    await first;
    await forced;

    expect(mocks.fetchWorkspaceBootstrap).toHaveBeenCalledTimes(2);
    const state = useWorkspaceBootstrapStore.getState();
    expect(state.frequent.map((a) => a.name)).toEqual(['变更后的常用']);
    // And the forced (post-mutation) answer is fresh, not branded stale.
    expect(state.dirty).toBe(false);
    expect(state.error).toBeNull();
  });

  it('force reload still resolves with fresh data when the drained request fails', async () => {
    mocks.fetchWorkspaceBootstrap
      .mockRejectedValueOnce({ response: { data: { detail: 'first boom' } } })
      .mockResolvedValueOnce(payload({ frequent: [summary({ id: 9, name: '重试后的常用' })] }));

    const first = useWorkspaceBootstrapStore.getState().load().catch(() => undefined);
    const forced = useWorkspaceBootstrapStore.getState().load(true);
    await first;
    await forced;

    expect(mocks.fetchWorkspaceBootstrap).toHaveBeenCalledTimes(2);
    expect(useWorkspaceBootstrapStore.getState().frequent.map((a) => a.name)).toEqual(['重试后的常用']);
    expect(useWorkspaceBootstrapStore.getState().error).toBeNull();
  });
});

describe('lookups', () => {
  it('finds a row by id and by slug across every group', async () => {
    await useWorkspaceBootstrapStore.getState().load();
    const store = useWorkspaceBootstrapStore.getState();

    expect(store.summaryById(3)?.name).toBe('合同助手');
    expect(store.summaryById(10)?.name).toBe('OA密码修改');
    expect(store.summaryById(1)?.name).toBe('问数小安'); // the main agent
    expect(store.summaryById(999)).toBeUndefined();
    expect(store.summaryBySlug('slug-3')?.id).toBe(3);
    expect(store.summaryBySlug(undefined)).toBeUndefined();
  });
});

describe('toggleFavorite', () => {
  it('adds the row to 收藏 and patches it everywhere else', async () => {
    await useWorkspaceBootstrapStore.getState().load();
    useApplicationEntityStore.getState().upsertManage({
      ...(payload().frequent[0] as any),
      runtime_type: 'agent', provider_key: 'feishu_aily',
      identity_mode: 'user', execution_mode: 'interactive', capabilities: {},
    });

    await useWorkspaceBootstrapStore.getState().toggleFavorite(2);

    const state = useWorkspaceBootstrapStore.getState();
    expect(state.favorites.map((a) => a.id)).toEqual([2]);
    expect(state.frequent.find((a) => a.id === 2)?.is_favorite).toBe(true);
    // The route-resolved entity follows along, so the chat header's ✩ updates.
    expect(useApplicationEntityStore.getState().get(2)?.is_favorite).toBe(true);
    expect(mocks.setApplicationFavorite).toHaveBeenCalledWith(2, true);
  });

  it('accepts a caller-supplied fallback row that is in no group', async () => {
    await useWorkspaceBootstrapStore.getState().load();
    const orphan = summary({ id: 55, name: '刚建的智能体' });

    await useWorkspaceBootstrapStore.getState().toggleFavorite(55, orphan);

    expect(useWorkspaceBootstrapStore.getState().favorites.map((a) => a.id)).toEqual([55]);
    expect(mocks.setApplicationFavorite).toHaveBeenCalledWith(55, true);
  });

  it('does nothing when the row is unknown and no fallback is given', async () => {
    await useWorkspaceBootstrapStore.getState().load();

    await useWorkspaceBootstrapStore.getState().toggleFavorite(1234);

    expect(mocks.setApplicationFavorite).not.toHaveBeenCalled();
    expect(useWorkspaceBootstrapStore.getState().favorites).toEqual([]);
  });

  it('reverts 收藏 exactly when the server rejects the toggle', async () => {
    mocks.setApplicationFavorite.mockRejectedValue(new Error('nope'));
    await useWorkspaceBootstrapStore.getState().load();

    await useWorkspaceBootstrapStore.getState().toggleFavorite(2);

    const state = useWorkspaceBootstrapStore.getState();
    expect(state.favorites).toEqual([]);
    // The rollback patches THIS row's flag only (三次复审 P1-R3) — an
    // explicit `false`, not a resurrection of the whole previous snapshot.
    expect(state.frequent.find((a) => a.id === 2)?.is_favorite).toBe(false);
    expect(state.error).toBe('收藏操作失败');
    // The failure marks the groups stale so the server recomputes membership.
    expect(state.dirty).toBe(true);
  });

  it('keeps a successful toggle intact when a slower toggle for another app fails', async () => {
    await useWorkspaceBootstrapStore.getState().load();
    const state = useWorkspaceBootstrapStore.getState();
    useApplicationEntityStore.getState().upsertManage({
      ...(state.frequent[0] as any),
      runtime_type: 'agent', provider_key: 'feishu_aily',
      identity_mode: 'user', execution_mode: 'interactive', capabilities: {},
    });

    // 收藏 2 慢且最终失败，收藏 3 快且成功 — 2 的回滚绝不能抹掉 3.
    let rejectFirst: (e: unknown) => void = () => {};
    mocks.setApplicationFavorite
      .mockImplementationOnce(() => new Promise((_resolve, reject) => { rejectFirst = reject; }))
      .mockResolvedValueOnce({ application_id: 3, is_favorite: true });

    const slow = useWorkspaceBootstrapStore.getState().toggleFavorite(2);
    await useWorkspaceBootstrapStore.getState().toggleFavorite(3);

    rejectFirst(new Error('nope'));
    await slow.catch(() => undefined);

    const after = useWorkspaceBootstrapStore.getState();
    // B survived: the failed rollback of A must not restore the snapshot
    // from before B (三次复审 P1-R3). App 3 lives in 最近使用; it joined the
    // 收藏 group, and its flag stays flipped everywhere.
    expect(after.recent.find((a) => a.id === 3)?.is_favorite).toBe(true);
    expect(after.favorites.map((a) => a.id)).toEqual([3]);
    expect(after.frequent.find((a) => a.id === 2)?.is_favorite).toBe(false);
    expect(after.error).toBe('收藏操作失败');
  });
});

describe('patch / remove', () => {
  it('patches a row in every group it appears in', async () => {
    await useWorkspaceBootstrapStore.getState().load();

    useWorkspaceBootstrapStore.getState().patch(2, { name: '运维小安 V2' });

    expect(useWorkspaceBootstrapStore.getState().frequent[0].name).toBe('运维小安 V2');
  });

  it('drops a deleted row — including the main agent — from every group', async () => {
    await useWorkspaceBootstrapStore.getState().load();

    useWorkspaceBootstrapStore.getState().remove(1);
    expect(useWorkspaceBootstrapStore.getState().defaultApplication).toBeNull();

    useWorkspaceBootstrapStore.getState().remove(3);
    expect(useWorkspaceBootstrapStore.getState().recent).toEqual([]);
  });
});
