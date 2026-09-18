import { create } from 'zustand';
import {
  captureSessionGeneration,
  registerSessionReset,
  sessionStillCurrent,
} from '@/stores/resetSessionState';
import { api } from '@/services/api';

interface MarketState {
  trending: any[];
  recommended: any[];
  favorites: any[];
  isLoading: boolean;
  loadTrending: () => Promise<void>;
  loadRecommended: () => Promise<void>;
  loadFavorites: () => Promise<void>;
  addFavorite: (templateId: number) => Promise<void>;
  removeFavorite: (favoriteId: number) => Promise<void>;
  addReview: (templateId: number, rating: number, comment: string) => Promise<void>;
  /** Back to the inert state — called by resetSessionScopedState (P0-2). */
  reset: () => void;
}

export const useMarketStore = create<MarketState>((set, get) => ({
  trending: [],
  recommended: [],
  favorites: [],
  isLoading: false,

  loadTrending: async () => {
    const generation = captureSessionGeneration();
    try {
      set({ isLoading: true });
      const response = await api.get<any>('/marketplace/trending/');
      if (!sessionStillCurrent(generation)) return;
      set({ trending: Array.isArray(response) ? response : response.results ?? [] });
    } catch (error) {
      console.error('Failed to load trending:', error);
    } finally {
      if (sessionStillCurrent(generation)) set({ isLoading: false });
    }
  },

  loadRecommended: async () => {
    const generation = captureSessionGeneration();
    try {
      const response = await api.get<any>('/marketplace/recommended/');
      if (!sessionStillCurrent(generation)) return;
      set({ recommended: Array.isArray(response) ? response : response.results ?? [] });
    } catch (error) {
      console.error('Failed to load recommended:', error);
    }
  },

  reset: () => set({
    trending: [],
    recommended: [],
    favorites: [],
    isLoading: false,
  }),

  loadFavorites: async () => {
    const generation = captureSessionGeneration();
    try {
      const response = await api.get<any>('/marketplace/favorites/');
      if (!sessionStillCurrent(generation)) return;
      set({ favorites: Array.isArray(response) ? response : response.results ?? [] });
    } catch (error) {
      console.error('Failed to load favorites:', error);
    }
  },

  addFavorite: async (templateId: number) => {
    const generation = captureSessionGeneration();
    try {
      const response = await api.post('/marketplace/favorites/', { template: templateId });
      if (!sessionStillCurrent(generation)) return;
      set({ favorites: [...get().favorites, response] });
    } catch (error) {
      console.error('Failed to add favorite:', error);
      throw error;
    }
  },

  removeFavorite: async (favoriteId: number) => {
    const generation = captureSessionGeneration();
    try {
      await api.delete(`/marketplace/favorites/${favoriteId}/`);
      if (!sessionStillCurrent(generation)) return;
      set({ favorites: get().favorites.filter(f => f.id !== favoriteId) });
    } catch (error) {
      console.error('Failed to remove favorite:', error);
      throw error;
    }
  },

  addReview: async (templateId: number, rating: number, comment: string) => {
    const generation = captureSessionGeneration();
    try {
      await api.post('/marketplace/reviews/', { template: templateId, rating, comment });
      if (!sessionStillCurrent(generation)) return;
    } catch (error) {
      console.error('Failed to add review:', error);
      throw error;
    }
  },
}));

// Market favourites are per-user (P0-2).
registerSessionReset(() => useMarketStore.getState().reset());
