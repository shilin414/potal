import { beforeEach, describe, expect, it } from 'vitest';
import {
  RECENT_LIMIT,
  useWorkspaceStore,
  withRecent,
  workspaceStateOf,
} from '../useWorkspaceStore';

describe('withRecent', () => {
  it('moves the application to the front without duplicating', () => {
    expect(withRecent([2, 3], 3)).toEqual([3, 2]);
    expect(withRecent([2, 3], 4)).toEqual([4, 2, 3]);
  });

  it('caps the list', () => {
    const ids = Array.from({ length: RECENT_LIMIT + 5 }, (_, i) => i + 1);
    let recent: number[] = [];
    ids.forEach((id) => { recent = withRecent(recent, id); });
    expect(recent).toHaveLength(RECENT_LIMIT);
    expect(recent[0]).toBe(RECENT_LIMIT + 5);
  });
});

describe('useWorkspaceStore (UI state only)', () => {
  beforeEach(() => {
    useWorkspaceStore.getState().clearAll();
  });

  it('opens an application and records local recency', () => {
    useWorkspaceStore.getState().openApplication(7);
    const state = useWorkspaceStore.getState();
    expect(state.activeApplicationId).toBe(7);
    expect(state.recentApplicationIds).toEqual([7]);
  });

  it('records the workspace the user came from (§80 固定应用返回)', () => {
    const store = useWorkspaceStore.getState();
    store.openApplication(11); // chat workspace
    store.openApplication(42); // fixed app opened on top of it
    expect(useWorkspaceStore.getState().previousApplicationId).toBe(11);

    // Returning to the chat workspace re-points "previous" at the fixed app.
    store.openApplication(11);
    expect(useWorkspaceStore.getState().previousApplicationId).toBe(42);

    // Re-opening the SAME application must not clobber the return target
    // (catalog refreshes re-run the open effect).
    store.openApplication(11);
    expect(useWorkspaceStore.getState().previousApplicationId).toBe(42);
  });

  it('keeps draft, conversation and scroll per application', () => {
    const store = useWorkspaceStore.getState();
    store.setDraft(1, '销售草稿');
    store.setDraft(2, '采购草稿');
    store.rememberConversation(1, 101);
    store.setScrollTop(1, 320);

    const state = useWorkspaceStore.getState();
    expect(workspaceStateOf(state.workspaces, 1)).toMatchObject({
      draft: '销售草稿', conversationId: 101, scrollTop: 320,
    });
    // Switching away and back restores each application's own workspace.
    expect(workspaceStateOf(state.workspaces, 2).draft).toBe('采购草稿');
    expect(workspaceStateOf(state.workspaces, 2).conversationId).toBeNull();
  });

  it('returns an inert default for an unknown application', () => {
    expect(workspaceStateOf(useWorkspaceStore.getState().workspaces, 999))
      .toMatchObject({ conversationId: null, draft: '', scrollTop: 0 });
    expect(workspaceStateOf(useWorkspaceStore.getState().workspaces, null).draft)
      .toBe('');
  });

  it('queues a prompt once and consumes it exactly once', () => {
    const store = useWorkspaceStore.getState();
    store.queuePrompt(5, '帮我分析');
    expect(useWorkspaceStore.getState().takePrompt(5)).toBe('帮我分析');
    expect(useWorkspaceStore.getState().takePrompt(5)).toBeNull();
  });

  it('does not hand a prompt to a different application', () => {
    useWorkspaceStore.getState().queuePrompt(5, '帮我分析');
    expect(useWorkspaceStore.getState().takePrompt(6)).toBeNull();
    expect(useWorkspaceStore.getState().pendingPrompt).toEqual({
      applicationId: 5, text: '帮我分析',
    });
  });

  it('starting a new conversation resets that application only', () => {
    const store = useWorkspaceStore.getState();
    store.rememberConversation(1, 101);
    store.setDraft(1, 'x');
    store.setDraft(2, 'y');

    useWorkspaceStore.getState().startNewConversation(1);

    const state = useWorkspaceStore.getState();
    expect(workspaceStateOf(state.workspaces, 1)).toMatchObject({
      conversationId: null, draft: '', scrollTop: 0,
    });
    expect(workspaceStateOf(state.workspaces, 2).draft).toBe('y');
  });
});
