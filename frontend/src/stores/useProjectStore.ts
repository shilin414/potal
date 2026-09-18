import { create } from 'zustand';
import {
  captureSessionGeneration,
  registerSessionReset,
  sessionStillCurrent,
} from '@/stores/resetSessionState';
import { api } from '@/services/api';

export interface Project {
  id: number;
  title: string;
  description: string;
  status: string;
  thumbnail: string;
  application_id?: number | null;
  application_slug?: string | null;
  application_kind?: string | null;
  conversation_id?: number | null;
  working_directory?: string;
  source?: 'application' | 'workflow';
  created_at: string;
  updated_at: string;
}

interface ProjectState {
  projects: Project[];
  currentProject: any;
  isLoading: boolean;
  loadProjects: () => Promise<void>;
  loadProject: (id: number) => Promise<void>;
  createProject: (data: any) => Promise<Project>;
  updateProject: (id: number, data: any) => Promise<void>;
  deleteProject: (id: number) => Promise<void>;
  /** Back to the inert state — called by resetSessionScopedState (P0-2). */
  reset: () => void;
}

export const useProjectStore = create<ProjectState>((set, get) => ({
  projects: [],
  currentProject: null,
  isLoading: false,

  reset: () => set({ projects: [], currentProject: null, isLoading: false }),

  loadProjects: async () => {
    const generation = captureSessionGeneration();
    try {
      set({ isLoading: true });
      const response = await api.get<any>('/projects/');
      if (!sessionStillCurrent(generation)) return;
      set({ projects: Array.isArray(response) ? response : response.results ?? [] });
    } catch (error) {
      console.error('Failed to load projects:', error);
    } finally {
      if (sessionStillCurrent(generation)) set({ isLoading: false });
    }
  },

  loadProject: async (id: number) => {
    const generation = captureSessionGeneration();
    try {
      set({ isLoading: true });
      const response = await api.get(`/projects/${id}/`);
      if (!sessionStillCurrent(generation)) return;
      set({ currentProject: response });
    } catch (error) {
      console.error('Failed to load project:', error);
    } finally {
      if (sessionStillCurrent(generation)) set({ isLoading: false });
    }
  },

  createProject: async (data) => {
    const generation = captureSessionGeneration();
    try {
      const response = await api.post('/projects/', data);
      if (!sessionStillCurrent(generation)) throw new Error('session changed');
      const { projects } = get();
      set({ projects: [response, ...projects] });
      return response;
    } catch (error) {
      console.error('Failed to create project:', error);
      throw error;
    }
  },

  updateProject: async (id: number, data) => {
    const generation = captureSessionGeneration();
    try {
      await api.put(`/projects/${id}/`, data);
      if (!sessionStillCurrent(generation)) return;
      await get().loadProjects();
    } catch (error) {
      console.error('Failed to update project:', error);
      throw error;
    }
  },

  deleteProject: async (id: number) => {
    const generation = captureSessionGeneration();
    try {
      await api.delete(`/projects/${id}/`);
      if (!sessionStillCurrent(generation)) return;
      const { projects, currentProject } = get();
      set({
        projects: projects.filter(p => p.id !== id),
        currentProject: currentProject?.id === id ? null : currentProject,
      });
    } catch (error) {
      console.error('Failed to delete project:', error);
      throw error;
    }
  },
}));

// Projects are per-user (P0-2).
registerSessionReset(() => useProjectStore.getState().reset());
