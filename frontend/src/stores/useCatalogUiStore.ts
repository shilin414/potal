/**
 * useCatalogUiStore — the 应用中心 category filter, and nothing else.
 *
 * The rail (AppCategoriesSidebar) and the card grid (AppsPage) share one
 * selection, so it cannot live in either component. It used to sit in
 * `useApplicationCatalogStore`, which also held the whole catalog mirror; that
 * mirror is gone (see `useWorkspaceBootstrapStore`), and a UI selection must
 * not be smuggled into a data store just to keep a component compiling.
 */
import { create } from 'zustand';

interface CatalogUiState {
  /** Selected category slug for 应用中心; null = 全部应用. */
  fixedCategory: string | null;
  setFixedCategory: (category: string | null) => void;
}

export const useCatalogUiStore = create<CatalogUiState>()((set) => ({
  fixedCategory: null,
  setFixedCategory: (fixedCategory) => set({ fixedCategory }),
}));
