/**
 * useCatalogUiStore — the 应用中心 category filter, and nothing else.
 *
 * The rail (AppCategoriesSidebar) and the card grid (AppsPage) share one
 * selection, so it cannot live in either component. It used to sit in
 * `useApplicationCatalogStore`, which also held the whole catalog mirror; that
 * mirror is gone (see `useWorkspaceBootstrapStore`), and a UI selection must
 * not be smuggled into a data store just to keep a component compiling.
 */
import { create } from "zustand";
import { registerSessionReset } from "@/stores/resetSessionState";

interface CatalogUiState {
  /** Selected category slug for 智能体中心; null = 全部智能体. */
  agentCategory: string | null;
  setAgentCategory: (category: string | null) => void;
  /** Selected category slug for 应用中心; null = 全部应用. */
  fixedCategory: string | null;
  setFixedCategory: (category: string | null) => void;
  /** Back to the inert state — called by resetSessionScopedState (P0-2). */
  reset: () => void;
}

export const useCatalogUiStore = create<CatalogUiState>()((set) => ({
  agentCategory: null,
  setAgentCategory: (agentCategory) => set({ agentCategory }),
  fixedCategory: null,
  setFixedCategory: (fixedCategory) => set({ fixedCategory }),
  reset: () => set({ agentCategory: null, fixedCategory: null }),
}));

// A category selection is not user data, but it is stale across identities in
// exactly the same way, and resetting it is free (P0-2).
registerSessionReset(() => useCatalogUiStore.getState().reset());
