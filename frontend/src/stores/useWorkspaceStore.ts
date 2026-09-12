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
}

export interface PendingPrompt {
  applicationId: number;
  text: string;
}

export const EMPTY_WORKSPACE_STATE: ApplicationWorkspaceState = {
  conversationId: null,
  draft: '',
  scrollTop: 0,
  updatedAt: 0,
  newConversationTick: 0,
};

/** How many applications the local 最近使用 list keeps. */
export const RECENT_LIMIT = 8;

interface WorkspaceStore {
  activeApplicationId: number | null;
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
      workspaces: {},
      recentApplicationIds: [],
      pendingPrompt: null,
      sidebarCollapsed: false,
      mobileNavOpen: false,

      openApplication: (applicationId) => set((state) => ({
        activeApplicationId: applicationId,
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
          newConversationTick:
            (state.workspaces[applicationId]?.newConversationTick || 0) + 1,
        }),
      })),

      setSidebarCollapsed: (sidebarCollapsed) => set({ sidebarCollapsed }),
      setMobileNavOpen: (mobileNavOpen) => set({ mobileNavOpen }),

      clearAll: () => set({
        activeApplicationId: null,
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
        workspaces: state.workspaces,
        recentApplicationIds: state.recentApplicationIds,
        sidebarCollapsed: state.sidebarCollapsed,
      }),
    },
  ),
);
