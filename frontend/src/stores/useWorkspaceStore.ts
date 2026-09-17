/**
 * useWorkspaceStore — UI-only state of the Workspace shell (架构文档 §28).
 *
 * Strictly UI state: which application is active in the shell, and per
 * application the last Conversation id / composer draft / scroll offset so
 * switching agents restores the workspace instead of resetting it (§29).
 *
 * Business facts (Conversation / Message / Run / Artifact) never live here —
 * they stay in the backend and are mirrored by useRunChatStore for rendering.
 */
import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { registerSessionReset } from '@/stores/resetSessionState';

export interface ApplicationWorkspaceState {
  /** Conversation the workspace should restore when reopened (null = new). */
  conversationId: number | null;
  /** Unsent composer text, kept per application. */
  draft: string;
  /** Last scroll offset of the message list, in px. */
  scrollTop: number;
  /** epoch ms of the last interaction — drives the local 最近使用 ordering. */
  updatedAt: number;
  /**
   * Bumped by startNewConversation so an ALREADY-MOUNTED workspace reacts:
   * navigating to the same /chat/:slug URL remounts nothing, so the renderer
   * watches this tick to reset to a fresh conversation (sidebar 新建).
   */
  newConversationTick: number;
  /**
   * 技能配置 selection for this application's composer (design report §9.4).
   *
   * Kept PER APPLICATION, like the draft: switching agent A → B → A must
   * restore A's own skill combination rather than leaking B's. It survives
   * sends (a multi-turn task keeps its skills) and is cleared only by 新任务,
   * which is the opposite of the draft's lifecycle.
   *
   * Ids, not names, so renaming a skill in 智能体市场 does not invalidate an
   * existing selection; stale ids are dropped at read time by
   * `resolveSelectedSkills`.
   */
  selectedSkillIds: string[];
}

export interface PendingPrompt {
  applicationId: number;
  text: string;
}

/**
 * The inert state unknown applications read back.
 *
 * Frozen on purpose: `workspaceStateOf` returns THIS object (not a copy) so a
 * Zustand selector keeps a stable identity and does not re-render forever.
 * That makes it shared across every application, so the nested array must not
 * be mutable — otherwise one stray `push` would edit the default for all of
 * them at once, silently.
 */
export const EMPTY_WORKSPACE_STATE: ApplicationWorkspaceState = {
  conversationId: null,
  draft: '',
  scrollTop: 0,
  updatedAt: 0,
  newConversationTick: 0,
  selectedSkillIds: Object.freeze([]) as unknown as string[],
};

/** How many applications the local 最近使用 list keeps. */
export const RECENT_LIMIT = 8;

interface WorkspaceStore {
  activeApplicationId: number | null;
  /**
   * The workspace the user came from (§80): a fixed application's
   * 返回工作台 goes here instead of reloading Home, so the chat
   * conversation / scroll / draft the user left behind is restored.
   */
  previousApplicationId: number | null;
  workspaces: Record<number, ApplicationWorkspaceState>;
  recentApplicationIds: number[];
  /** Prompt handed over by @mention routing, consumed exactly once. */
  pendingPrompt: PendingPrompt | null;
  sidebarCollapsed: boolean;
  mobileNavOpen: boolean;

  openApplication: (applicationId: number) => void;
  setActiveApplication: (applicationId: number | null) => void;
  rememberConversation: (applicationId: number, conversationId: number | null) => void;
  setDraft: (applicationId: number, draft: string) => void;
  setScrollTop: (applicationId: number, scrollTop: number) => void;
  /** Replace this application's 技能配置 selection (already catalog-ordered). */
  setSelectedSkillIds: (applicationId: number, skillIds: string[]) => void;
  touchRecent: (applicationId: number) => void;
  queuePrompt: (applicationId: number, text: string) => void;
  takePrompt: (applicationId: number) => string | null;
  startNewConversation: (applicationId: number) => void;
  setSidebarCollapsed: (collapsed: boolean) => void;
  setMobileNavOpen: (open: boolean) => void;
  clearAll: () => void;
}

/** Read one application's workspace state without allocating. */
export function workspaceStateOf(
  workspaces: Record<number, ApplicationWorkspaceState>,
  applicationId: number | null | undefined,
): ApplicationWorkspaceState {
  if (applicationId == null) return EMPTY_WORKSPACE_STATE;
  return workspaces[applicationId] || EMPTY_WORKSPACE_STATE;
}

/** Pure recency update, exported so it can be unit tested in isolation. */
export function withRecent(
  recent: number[],
  applicationId: number,
): number[] {
  return [applicationId, ...recent.filter((id) => id !== applicationId)]
    .slice(0, RECENT_LIMIT);
}

const patch = (
  workspaces: Record<number, ApplicationWorkspaceState>,
  applicationId: number,
  next: Partial<ApplicationWorkspaceState>,
): Record<number, ApplicationWorkspaceState> => ({
  ...workspaces,
  [applicationId]: {
    ...(workspaces[applicationId] || EMPTY_WORKSPACE_STATE),
    ...next,
    updatedAt: Date.now(),
  },
});

export const useWorkspaceStore = create<WorkspaceStore>()(
  persist(
    (set, get) => ({
      activeApplicationId: null,
      previousApplicationId: null,
      workspaces: {},
      recentApplicationIds: [],
      pendingPrompt: null,
      sidebarCollapsed: false,
      mobileNavOpen: false,

      openApplication: (applicationId) => set((state) => ({
        activeApplicationId: applicationId,
        // Only a real switch records a return target; re-opening the same
        // application (catalog refresh re-runs the effect) must not clobber it.
        previousApplicationId: state.activeApplicationId === applicationId
          ? state.previousApplicationId
          : state.activeApplicationId,
        recentApplicationIds: withRecent(state.recentApplicationIds, applicationId),
      })),

      setActiveApplication: (applicationId) => {
        if (applicationId == null) {
          set({ activeApplicationId: null });
          return;
        }
        get().openApplication(applicationId);
      },

      rememberConversation: (applicationId, conversationId) => set((state) => ({
        workspaces: patch(state.workspaces, applicationId, { conversationId }),
      })),

      setDraft: (applicationId, draft) => set((state) => ({
        workspaces: patch(state.workspaces, applicationId, { draft }),
      })),

      setScrollTop: (applicationId, scrollTop) => set((state) => ({
        workspaces: patch(state.workspaces, applicationId, { scrollTop }),
      })),

      setSelectedSkillIds: (applicationId, skillIds) => set((state) => ({
        // Copy defensively: the caller may hand us a frozen or reused array,
        // and the persisted store must own its own value.
        workspaces: patch(state.workspaces, applicationId, {
          selectedSkillIds: [...skillIds],
        }),
      })),

      touchRecent: (applicationId) => set((state) => ({
        recentApplicationIds: withRecent(state.recentApplicationIds, applicationId),
      })),

      queuePrompt: (applicationId, text) => set({
        pendingPrompt: { applicationId, text },
        recentApplicationIds: withRecent(
          get().recentApplicationIds, applicationId),
      }),

      takePrompt: (applicationId) => {
        const pending = get().pendingPrompt;
        if (!pending || pending.applicationId !== applicationId) return null;
        set({ pendingPrompt: null });
        return pending.text;
      },

      startNewConversation: (applicationId) => set((state) => ({
        activeApplicationId: applicationId,
        workspaces: patch(state.workspaces, applicationId, {
          conversationId: null, draft: '', scrollTop: 0,
          // 新任务 means a NEW task context, so the previous task's skill
          // combination must not be inherited (design report §9.4). This is
          // the ONLY place skills are cleared — a send deliberately keeps them.
          selectedSkillIds: [],
          newConversationTick:
            (state.workspaces[applicationId]?.newConversationTick || 0) + 1,
        }),
      })),

      setSidebarCollapsed: (sidebarCollapsed) => set({ sidebarCollapsed }),
      setMobileNavOpen: (mobileNavOpen) => set({ mobileNavOpen }),

      clearAll: () => set({
        activeApplicationId: null,
        previousApplicationId: null,
        workspaces: {},
        recentApplicationIds: [],
        pendingPrompt: null,
        mobileNavOpen: false,
      }),
    }),
    {
      name: 'workspace-storage',
      // pendingPrompt is transient: persisting it would replay an unsent
      // instruction after a reload.
      partialize: (state) => ({
        activeApplicationId: state.activeApplicationId,
        previousApplicationId: state.previousApplicationId,
        workspaces: state.workspaces,
        recentApplicationIds: state.recentApplicationIds,
        sidebarCollapsed: state.sidebarCollapsed,
      }),
    },
  ),
);

// Drafts, conversation ids, scroll offsets, skill selections and 最近使用 —
// the most obviously personal state in the app, and the one that is
// `persist`ed (so it survives even a real reload). Forget it on a user
// switch (P0-2).
registerSessionReset(() => useWorkspaceStore.getState().clearAll());
