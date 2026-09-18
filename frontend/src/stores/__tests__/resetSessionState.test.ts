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
 *     from the local-admin login and as a string from the Feishu exchange;
 *   · the persisted organization/workspace storage keys are wiped too, and
 *     real cross-user preferences (theme) survive (三次复审 P0-R2).
 */
// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  bootstrap: vi.fn(),
  resolve: vi.fn(),
  favorite: vi.fn(),
  conversationGet: vi.fn(),
  runApiGet: vi.fn(),
  runApiArtifacts: vi.fn(),
  runApiCreate: vi.fn(),
  runStream: vi.fn(),
}));

vi.mock('@/services/runApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/runApi')>();
  return {
    ...actual,
    fetchWorkspaceBootstrap: mocks.bootstrap,
    resolveApplication: mocks.resolve,
    setApplicationFavorite: mocks.favorite,
    getRun: mocks.runApiGet,
    fetchRunArtifacts: mocks.runApiArtifacts,
    createRun: mocks.runApiCreate,
  };
});

vi.mock('@/services/axios', () => ({
  default: { get: mocks.conversationGet },
}));

vi.mock('@/services/runStream', () => ({
  openRunStream: mocks.runStream,
}));

import { resetSessionScopedState, isSameUser } from '@/stores/resetSessionState';
import { useAuthStore } from '@/stores/useAuthStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import { useRunChatStore } from '@/stores/useRunChatStore';
import { useOrganizationStore } from '@/stores/useOrganizationStore';
import { useConversationStore } from '@/stores/useConversationStore';
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
  mocks.conversationGet.mockReset();
  mocks.runApiGet.mockReset();
  mocks.runApiArtifacts.mockReset();
  mocks.runApiCreate.mockReset();
  mocks.runStream.mockReset();
  resetSessionScopedState();
  useAuthStore.setState({
    user: null, isAuthenticated: false, isLoggingOut: false, explicitlyLoggedOut: false,
  });
});

describe('resetSessionScopedState', () => {
  it('forgets the previous user in every session-scoped store', async () => {
    await useWorkspaceBootstrapStore.getState().load(true);
    useApplicationEntityStore.getState().upsertManage(app(7, 'A解析过的应用'));
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

describe('organization session isolation (三次复审 P0-R2)', () => {
  const persistedOrganizations = (raw: string | null): string | null => {
    const parsed = raw == null ? null : JSON.parse(raw);
    return parsed?.state?.currentOrganizationId ?? null;
  };

  it('forgets the previous user\u2019s organizations and their persisted selection', () => {
    useOrganizationStore.setState({
      organizations: [
        { id: 'org-a1', name: 'A 的组织一', slug: 'a1', role: 'owner' },
        { id: 'org-a2', name: 'A 的组织二', slug: 'a2', role: 'member' },
      ],
      currentOrganizationId: 'org-a2',
    });
    // The axios interceptor reads the PERSISTED key on every request and
    // sends it as X-Organization-ID — so the persisted copy is part of the
    // leak surface, not just the in-memory store.
    localStorage.setItem(
      'organization-storage',
      JSON.stringify({
        state: { organizations: [{ id: 'org-a2' }], currentOrganizationId: 'org-a2' },
        version: 0,
      }),
    );

    resetSessionScopedState();

    const org = useOrganizationStore.getState();
    expect(org.organizations).toEqual([]);
    expect(org.currentOrganizationId).toBeNull();
    // No stale X-Organization-ID can be read by the interceptor afterwards.
    expect(persistedOrganizations(localStorage.getItem('organization-storage'))).toBeNull();
    expect(localStorage.getItem('organization-storage')).toBeNull();
  });

  it('wipes the persisted organization/workspace keys even when the store module never loaded', () => {
    // Simulates the lazy-chunk scenario (三次复审 P0-R2): user A's session
    // wrote the key, but the resetter for this store was never registered in
    // the running bundle. The key removal must not depend on module loading.
    localStorage.setItem(
      'organization-storage',
      JSON.stringify({ state: { currentOrganizationId: 'org-a9' }, version: 0 }),
    );
    localStorage.setItem(
      'workspace-storage',
      JSON.stringify({ state: { workspaces: { 7: { draft: 'A 的草稿' } } }, version: 0 }),
    );

    resetSessionScopedState();

    expect(localStorage.getItem('organization-storage')).toBeNull();
    expect(localStorage.getItem('workspace-storage')).toBeNull();
  });

  it('drops an organization list that settles after the switch', async () => {
    let release: (value: unknown) => void = () => {};
    const { api } = await import('@/services/api');
    const get = vi.spyOn(api, 'get').mockReturnValueOnce(
      new Promise((resolve) => { release = resolve; }) as any);

    const pending = useOrganizationStore.getState().loadOrganizations();
    resetSessionScopedState();
    release([{ id: 'org-a', name: 'A', slug: 'a', role: 'owner' }]);
    await pending;

    expect(useOrganizationStore.getState().currentOrganizationId).toBeNull();
    expect(useOrganizationStore.getState().organizations).toEqual([]);
    get.mockRestore();
  });

  it('不清除真正跨用户的客户端偏好（主题）', () => {
    localStorage.setItem('theme-storage', JSON.stringify({ state: { theme: 'dark' }, version: 0 }));

    resetSessionScopedState();

    expect(localStorage.getItem('theme-storage')).not.toBeNull();
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

describe('session epoch -> legacy stores (四次复审 P0-R1/R3/R4)', () => {
  it('ignores a conversation list response that settles after the switch', async () => {
    let release: (value: unknown) => void = () => {};
    mocks.conversationGet.mockReturnValueOnce(
      new Promise((resolve) => { release = resolve; }));

    const pending = useConversationStore.getState().fetchConversations();
    resetSessionScopedState();
    release([{ id: 'a-conversation', title: 'A 的对话' }]);
    await pending;

    expect(useConversationStore.getState().conversations).toEqual([]);
    expect(localStorage.getItem('conversation-storage')).toBeNull();
  });

  it('ignores a conversation detail response that settles after the switch', async () => {
    let release: (value: unknown) => void = () => {};
    mocks.conversationGet.mockReturnValueOnce(
      new Promise((resolve) => { release = resolve; }));

    const pending = useConversationStore.getState().fetchConversationDetail('9');
    resetSessionScopedState();
    release({ id: 9, title: 'A 的对话', messages: [] });
    await pending;

    expect(useConversationStore.getState().currentConversation).toBeNull();
  });

  it('aborts a live legacy stream and drops an event already in flight', async () => {
    let handlers: Record<string, (...args: any[]) => void> | undefined;
    const abort = vi.fn();
    const controller = { abort } as unknown as AbortController;

    // Use the real streamChat mock through the module boundary: the store
    // registers the controller it returns, so `reset` must abort it.
    const sse = vi.spyOn(await import('@/services/sseClient'), 'streamChat');
    sse.mockImplementation((_id, _content, value) => {
      handlers = value as any;
      return controller;
    });
    useConversationStore.setState({
      currentConversation: {
        id: 'a-conversation', title: 'A', created_at: '', updated_at: '', messages: [],
      },
    });
    useConversationStore.getState().sendMessageStream('a-conversation', 'hello');

    resetSessionScopedState();
    handlers?.onEvent?.({
      sequence: 1,
      method: 'item/agentMessage/delta',
      params: { itemId: 'm1', delta: 'late A text' },
    });

    expect(abort).toHaveBeenCalled();
    expect(useConversationStore.getState().currentConversation).toBeNull();
    expect(useConversationStore.getState().agentActivity).toBeNull();
    expect(useConversationStore.getState().lastSequence).toBe(0);
  });

  it('closes a live Run stream and ignores an event queued before reset', async () => {
    let handlers: Record<string, (...args: any[]) => void> | undefined;
    const close = vi.fn();
    mocks.runStream.mockImplementation((_runId, value) => {
      handlers = value;
      return { close };
    });
    mocks.runApiGet.mockResolvedValue({
      id: 'run-a', conversation: 12, status: 'succeeded', output: { text: 'A' },
    });
    mocks.runApiArtifacts.mockResolvedValue([]);

    useRunChatStore.setState({
      conversations: {
        12: {
          id: 12,
          title: 'A',
          activeRunId: 'run-a',
          messages: [{
            id: 'run-run-a', role: 'assistant', content: '', created_at: '',
            runId: 'run-a', status: 'streaming',
          }],
        },
      },
      activeConversationId: 12,
    });
    // Open the stream through the store's public send path so the registry
    // has a real entry to close.
    mocks.runApiGet.mockResolvedValueOnce({
      id: 'run-a', conversation: 12, status: 'succeeded', output: { text: 'A' },
    });
    mocks.runApiCreate.mockResolvedValueOnce({
      id: 'run-a', conversation: 12, status: 'running',
    });
    await useRunChatStore.getState().sendMessage({
      applicationId: 1, conversationId: 12, content: 'A',
    });
    expect(handlers).toBeDefined();

    resetSessionScopedState();
    // B is now signed in and has their own empty conversation open. A late
    // event must not re-seed A's run-... bubble into B's active conversation.
    useRunChatStore.setState({
      conversations: {
        99: { id: 99, title: 'B', messages: [], activeRunId: null },
      },
      activeConversationId: 99,
    });
    handlers?.onEvent?.({
      run_id: 'run-a', event_type: 'content.delta', sequence: 1,
      payload: { text: 'A event after switch' },
    });

    expect(close).toHaveBeenCalled();
    expect(useRunChatStore.getState().conversations[99].messages).toEqual([]);
    expect(handlers).toBeDefined();
  });
});
