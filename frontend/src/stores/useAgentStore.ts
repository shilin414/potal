import { create } from 'zustand';
import { api } from '@/services/api';
import {
  captureSessionGeneration,
  registerSessionReset,
  sessionStillCurrent,
} from '@/stores/resetSessionState';

interface AgentCategory {
  id: number;
  name: string;
  slug: string;
  description: string;
  icon: string;
  agent_count: number;
}

export interface Agent {
  id: number;
  name: string;
  slug: string;
  description: string;
  icon: string;
  category_name: string;
  is_public: boolean;
  can_edit: boolean;
  can_delete: boolean;
  created_at: string;
}

interface AgentExecution {
  id: number;
  agent_name: string;
  input_data: any;
  output_data: any;
  status: string;
  created_at: string;
}

interface AgentState {
  categories: AgentCategory[];
  agents: Agent[];
  selectedCategory: string | null;
  searchQuery: string;
  executions: AgentExecution[];
  isLoading: boolean;
  loadCategories: () => Promise<void>;
  loadAgents: (category?: string) => Promise<void>;
  selectCategory: (slug: string | null) => void;
  setSearchQuery: (query: string) => void;
  executeAgent: (agentId: number, inputData: any) => Promise<AgentExecution>;
  loadMyExecutions: () => Promise<void>;
  /** Back to the inert state — called by resetSessionScopedState (P0-2). */
  reset: () => void;
}

export const useAgentStore = create<AgentState>((set, get) => ({
  categories: [],
  agents: [],
  selectedCategory: null,
  searchQuery: '',
  executions: [],
  isLoading: false,

  reset: () => set({
    categories: [],
    agents: [],
    selectedCategory: null,
    searchQuery: '',
    executions: [],
    isLoading: false,
  }),

  loadCategories: async () => {
    const generation = captureSessionGeneration();
    try {
      const response = await api.get<any>('/agents/categories/');
      if (!sessionStillCurrent(generation)) return;
      set({ categories: Array.isArray(response) ? response : response.results ?? [] });
    } catch (error) {
      console.error('Failed to load categories:', error);
    }
  },

  loadAgents: async (category?: string) => {
    const generation = captureSessionGeneration();
    try {
      set({ isLoading: true });
      const { searchQuery } = get();
      const params: any = {};
      if (category) params.category = category;
      if (searchQuery) params.search = searchQuery;
      const response = await api.get<any>('/agents/', params);
      if (!sessionStillCurrent(generation)) return;
      set({ agents: Array.isArray(response) ? response : response.results ?? [] });
    } catch (error) {
      console.error('Failed to load agents:', error);
    } finally {
      if (sessionStillCurrent(generation)) set({ isLoading: false });
    }
  },

  selectCategory: (slug: string | null) => {
    set({ selectedCategory: slug });
  },

  setSearchQuery: (query: string) => {
    set({ searchQuery: query });
  },

  executeAgent: async (agentId: number, inputData: any) => {
    const generation = captureSessionGeneration();
    try {
      const response = await api.post(`/agents/${agentId}/execute/`, {
        input_data: inputData,
      });
      if (!sessionStillCurrent(generation)) throw new Error('session changed');
      return response;
    } catch (error) {
      console.error('Failed to execute agent:', error);
      throw error;
    }
  },

  loadMyExecutions: async () => {
    const generation = captureSessionGeneration();
    try {
      const response = await api.get<any>('/agents/my_executions/');
      if (!sessionStillCurrent(generation)) return;
      set({ executions: Array.isArray(response) ? response : response.results ?? [] });
    } catch (error) {
      console.error('Failed to load executions:', error);
    }
  },
}));

// Legacy Django-side agent list + executions (P0-2).
registerSessionReset(() => useAgentStore.getState().reset());
