import { create } from 'zustand';
import {
  captureSessionGeneration,
  registerSessionReset,
  sessionStillCurrent,
} from '@/stores/resetSessionState';
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
    const generation = captureSessionGeneration();
    try {
      const response = await api.get<TemplateCategory[] | { results?: TemplateCategory[] }>(
        '/templates/categories/',
      );
      if (!sessionStillCurrent(generation)) return;
      set({ categories: unwrap(response) });
    } catch (error) {
      console.error('Failed to load case categories:', error);
    }
  },

  loadTemplate: async (id: number) => {
    const generation = captureSessionGeneration();
    set({ isLoadingTemplate: true });
    try {
      const response = await api.get<TemplateDetail>(`/templates/${id}/`);
      if (!sessionStillCurrent(generation)) throw new Error('session changed');
      set({ currentTemplate: response });
      return response;
    } catch (error) {
      if (!sessionStillCurrent(generation)) throw error;
      set({ currentTemplate: null });
      throw error;
    } finally {
      if (sessionStillCurrent(generation)) set({ isLoadingTemplate: false });
    }
  },

  clearCurrentTemplate: () => set({ currentTemplate: null }),

  loadTemplates: async (category?: string) => {
    const generation = captureSessionGeneration();
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
      if (!sessionStillCurrent(generation)) return;
      set({ templates: unwrap(response) });
    } catch (error) {
      if (!sessionStillCurrent(generation)) return;
      console.error('Failed to load cases:', error);
      set({ templates: [] });
    } finally {
      if (sessionStillCurrent(generation)) set({ isLoading: false });
    }
  },

  selectCategory: (slug) => set({ selectedCategory: slug }),
  setSearchQuery: (query) => set({ searchQuery: query }),
}));

// Template lists and the open template are per-user (P0-2).
registerSessionReset(() => useTemplateStore.getState().reset());
