/**
 * Session isolation (二次复审 P0-2).
 *
 * The bug: the workspace is a SPA and every login path ends in
 * `navigate('/')`, so the JS runtime — and with it every module-level Zustand
 * store — survives a user switch. User B inherited user A's default agent,
 * shortcut groups, resolved entities, drafts, conversation ids and
 * transcripts; two of those stores are even `persist`ed, so a reload did not
 * help either.
 *
 * What these tests pin:
 *   · one call wipes every registered session-scoped store;
 *   · `clear()` bumps a generation, so a response that was ALREADY IN FLIGHT
 *     for the previous user cannot land afterwards (clearing alone does
 *     not stop a running promise — this is the part that is easy to miss);
 *   · a re-login as the SAME user does NOT wipe (it would discard the
 *     user's own draft and open conversation);
 *   · the id comparison is string-based, because the id arrives as a number
 *     from the local-admin login and as a string from the Feishu exchange.
 */

import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  bootstrap: vi.fn(),
  resolve: vi.fn(),
  favorite: vi.fn(),
}));

vi.mock('@/services/runApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/runApi')>();
  return {
    ...actual,
    fetchWorkspaceBootstrap: mocks.bootstrap,
    resolveApplication: mocks.resolve,
    setApplicationFavorite: mocks.favorite,
  };
});

import { resetSessionScopedState, isSameUser } from '@/stores/resetSessionState';
import { useAuthStore } from '@/stores/useAuthStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import { useRunChatStore } from '@/stores/useRunChatStore';
import type { ApplicationSummary, V2Application } from '@/services/runApi';

const summary = (id: number, name: string): ApplicationSummary => ({
  id, slug: `slug-${id}`, name, description: '', icon: '', kind: 'chat',
});

const app = (id: number, name: string): V2Application => ({
  id, slug: `slug-${id}`, name, description: '', icon: '', kind: 'chat',
  runtime_type: 'agent', provider_key: 'feishu_aily', capabilities: {},
});

const BOOTSTRAP = {
  default_application: null,
  favorites: [summary(1, 'A的收藏')],
  frequent: [summary(2, 'A的常用')],
  recent: [summary(3, 'A的最近')],
  recommended: [],
  recent_fixed_apps: [],
  agent_categories: [{ slug: 'it', name: 'IT运维', count: 2 }],
  app_categories: [{ slug: 'oa', name: '办公', count: 1 }],
};

const user = (id: unknown) => ({
  id: id as string,
  username: `u${id}`,
  email: '',
  role: 'user',
  created_at: '2026-09-17T00:00:00Z',
});

beforeEach(() => {
  mocks.bootstrap.mockReset().mockResolvedValue(BOOTSTRAP);
  mocks.resolve.mockReset();
  mocks.favorite.mockReset();
  resetSessionScopedState();
  useAuthStore.setState({ user: null, isAuthenticated: false });
});

describe('resetSessionScopedState', () => {
  it('forgets the previous user in every session-scoped store', async () => {
    await useWorkspaceBootstrapStore.getState().load(true);
    useApplicationEntityStore.getState().upsert(app(7, 'A解析过的应用'));
    useWorkspaceStore.getState().setDraft(7, 'A还没发出去的草稿');
    useWorkspaceStore.setState({ activeApplicationId: 7, recentApplicationIds: [7, 8] });
    useRunChatStore.setState({
      conversations: { 12: { id: 12, title: 'A的对话', messages: [], activeRunId: null } },
      activeConversationId: 12,
    });

    resetSessionScopedState();

    const bootstrap = useWorkspaceBootstrapStore.getState();
    expect(bootstrap.favorites).toEqual([]);
    expect(bootstrap.frequent).toEqual([]);
    expect(bootstrap.recent).toEqual([]);
    // The fields the old `clear()` forgot (二次复审 §7): the two category
    // rails and the loading flag.
    expect(bootstrap.agentCategories).toEqual([]);
    expect(bootstrap.appCategories).toEqual([]);
    expect(bootstrap.isLoading).toBe(false);
    expect(bootstrap.loadedAt).toBe(0);

    expect(useApplicationEntityStore.getState().byId[7]).toBeUndefined();
    expect(useApplicationEntityStore.getState().bySlug['slug-7']).toBeUndefined();
    expect(useWorkspaceStore.getState().workspaces[7]).toBeUndefined();
    expect(useWorkspaceStore.getState().recentApplicationIds).toEqual([]);
    expect(useRunChatStore.getState().conversations).toEqual({});
    expect(useRunChatStore.getState().activeConversationId).toBeNull();
  });

  it('drops a bootstrap response that was in flight for the PREVIOUS user', async () => {
    // Release the response by hand so the switch happens mid-flight — the
    // exact interleaving that made `clear()` alone insufficient.
    let release: (value: unknown) => void = () => {};
    mocks.bootstrap.mockReturnValueOnce(
      new Promise((done) => { release = done; }));

    const inflight = useWorkspaceBootstrapStore.getState().load(true);
    // A logs out / B logs in while the request is still travelling.
    resetSessionScopedState();
    release(BOOTSTRAP);
    await inflight.catch(() => undefined);

    // The answer belonged to A. It must not become B's shortcuts.
    expect(useWorkspaceBootstrapStore.getState().favorites).toEqual([]);
    expect(useWorkspaceBootstrapStore.getState().loadedAt).toBe(0);
  });

  it('drops an entity resolve that was in flight for the PREVIOUS user', async () => {
    let release: (value: unknown) => void = () => {};
    mocks.resolve.mockReturnValueOnce(
      new Promise((done) => { release = done; }));

    const inflight = useApplicationEntityStore.getState().ensure(42);
    resetSessionScopedState();
    release(app(42, 'A的应用'));
    await inflight.catch(() => undefined);

    expect(useApplicationEntityStore.getState().byId[42]).toBeUndefined();
  });

  it('is wired into every login path through acceptAuthenticatedUser', () => {
    useWorkspaceStore.getState().setDraft(1, 'A的草稿');

    // A different user ⇒ wipe.
    useAuthStore.getState().acceptAuthenticatedUser(user('A'));
    useWorkspaceStore.getState().setDraft(1, 'A的草稿');
    useAuthStore.getState().acceptAuthenticatedUser(user('B'));
    expect(useWorkspaceStore.getState().workspaces[1]).toBeUndefined();

    // The SAME user again ⇒ keep (a re-login must not discard their work).
    useWorkspaceStore.getState().setDraft(1, 'B的草稿');
    useAuthStore.getState().acceptAuthenticatedUser(user('B'));
    expect(useWorkspaceStore.getState().workspaces[1]?.draft).toBe('B的草稿');
  });

  it('wipes on logout', () => {
    useWorkspaceStore.getState().setDraft(1, 'A的草稿');
    useAuthStore.getState().clearAuth();

    expect(useAuthStore.getState().user).toBeNull();
    expect(useWorkspaceStore.getState().workspaces[1]).toBeUndefined();
  });
});

describe('isSameUser', () => {
  it('treats the same id as one identity across number/string shapes', () => {
    // The local-admin login builds `id` with String(); the Feishu exchange
    // returns it as whatever the identity endpoint sent. `===` would call
    // these two different users and reset a session that never changed.
    expect(isSameUser({ id: 7 }, { id: '7' })).toBe(true);
    expect(isSameUser({ id: '7' }, { id: 7 })).toBe(true);
  });

  it('treats a different id, or a missing one, as a different session', () => {
    expect(isSameUser({ id: 7 }, { id: 8 })).toBe(false);
    expect(isSameUser({ id: 7 }, null)).toBe(false);
    expect(isSameUser({ id: undefined }, { id: undefined })).toBe(false);
  });
});
