import { create } from 'zustand';
import { registerSessionReset } from '@/stores/resetSessionState';
import { api } from '@/services/api';
import type {
  TemplateCategory,
  TemplateDetail,
  TemplateSummary,
} from '@/types/template';

interface TemplateState {
  categories: TemplateCategory[];
  templates: TemplateSummary[];
  currentTemplate: TemplateDetail | null;
  isLoadingTemplate: boolean;
  selectedCategory: string | null;
  searchQuery: string;
  isLoading: boolean;
  loadCategories: () => Promise<void>;
  loadTemplates: (category?: string) => Promise<void>;
  loadTemplate: (id: number) => Promise<TemplateDetail>;
  clearCurrentTemplate: () => void;
  selectCategory: (slug: string | null) => void;
  setSearchQuery: (query: string) => void;
  /** Back to the inert state — called by resetSessionScopedState (P0-2). */
  reset: () => void;
}

const unwrap = <T,>(response: T[] | { results?: T[] }): T[] =>
  Array.isArray(response) ? response : response.results ?? [];

export const useTemplateStore = create<TemplateState>((set, get) => ({
  categories: [],
  templates: [],
  currentTemplate: null,
  isLoadingTemplate: false,
  selectedCategory: null,
  searchQuery: '',
  isLoading: false,

  reset: () => set({
    categories: [],
    templates: [],
    currentTemplate: null,
    isLoadingTemplate: false,
    selectedCategory: null,
    searchQuery: '',
    isLoading: false,
  }),

  loadCategories: async () => {
    try {
      const response = await api.get<TemplateCategory[] | { results?: TemplateCategory[] }>(
        '/templates/categories/',
      );
      set({ categories: unwrap(response) });
    } catch (error) {
      console.error('Failed to load case categories:', error);
    }
  },

  loadTemplate: async (id: number) => {
    set({ isLoadingTemplate: true });
    try {
      const response = await api.get<TemplateDetail>(`/templates/${id}/`);
      set({ currentTemplate: response });
      return response;
    } catch (error) {
      set({ currentTemplate: null });
      throw error;
    } finally {
      set({ isLoadingTemplate: false });
    }
  },

  clearCurrentTemplate: () => set({ currentTemplate: null }),

  loadTemplates: async (category?: string) => {
    set({ isLoading: true });
    try {
      const { searchQuery } = get();
      const params: Record<string, string> = {};
      if (category) params.category = category;
      if (searchQuery.trim()) params.search = searchQuery.trim();
      const response = await api.get<TemplateSummary[] | { results?: TemplateSummary[] }>(
        '/templates/',
        { params },
      );
      set({ templates: unwrap(response) });
    } catch (error) {
      console.error('Failed to load cases:', error);
      set({ templates: [] });
    } finally {
      set({ isLoading: false });
    }
  },

  selectCategory: (slug) => set({ selectedCategory: slug }),
  setSearchQuery: (query) => set({ searchQuery: query }),
}));

// Template lists and the open template are per-user (P0-2).
registerSessionReset(() => useTemplateStore.getState().reset());
