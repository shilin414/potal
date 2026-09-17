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
}));

vi.mock('@/services/runApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/services/runApi')>()),
  fetchWorkspaceBootstrap: mocks.fetchWorkspaceBootstrap,
  setApplicationFavorite: mocks.setApplicationFavorite,
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
    useApplicationEntityStore.getState().upsert({
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
    expect(state.frequent.find((a) => a.id === 2)?.is_favorite).toBeUndefined();
    expect(state.error).toBe('收藏操作失败');
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
