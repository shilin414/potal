import { create } from 'zustand';
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
}

export const useMarketStore = create<MarketState>((set, get) => ({
  trending: [],
  recommended: [],
  favorites: [],
  isLoading: false,

  loadTrending: async () => {
    try {
      set({ isLoading: true });
      const response = await api.get<any>('/marketplace/trending/');
      set({ trending: Array.isArray(response) ? response : response.results ?? [] });
    } catch (error) {
      console.error('Failed to load trending:', error);
    } finally {
      set({ isLoading: false });
    }
  },

  loadRecommended: async () => {
    try {
      const response = await api.get<any>('/marketplace/recommended/');
      set({ recommended: Array.isArray(response) ? response : response.results ?? [] });
    } catch (error) {
      console.error('Failed to load recommended:', error);
    }
  },

  loadFavorites: async () => {
    try {
      const response = await api.get<any>('/marketplace/favorites/');
      set({ favorites: Array.isArray(response) ? response : response.results ?? [] });
    } catch (error) {
      console.error('Failed to load favorites:', error);
    }
  },

  addFavorite: async (templateId: number) => {
    try {
      const response = await api.post('/marketplace/favorites/', { template: templateId });
      set({ favorites: [...get().favorites, response] });
    } catch (error) {
      console.error('Failed to add favorite:', error);
      throw error;
    }
  },

  removeFavorite: async (favoriteId: number) => {
    try {
      await api.delete(`/marketplace/favorites/${favoriteId}/`);
      set({ favorites: get().favorites.filter(f => f.id !== favoriteId) });
    } catch (error) {
      console.error('Failed to remove favorite:', error);
      throw error;
    }
  },

  addReview: async (templateId: number, rating: number, comment: string) => {
    try {
      await api.post('/marketplace/reviews/', { template: templateId, rating, comment });
    } catch (error) {
      console.error('Failed to add review:', error);
      throw error;
    }
  },
}));