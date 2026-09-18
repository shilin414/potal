import { create } from 'zustand';
import {
  captureSessionGeneration,
  registerSessionReset,
  sessionStillCurrent,
} from '@/stores/resetSessionState';
import type { AppItem, AppCategory } from '@/types';
import { api } from '@/services/api';

interface AppState {
  apps: AppItem[];
  categories: AppCategory[];
  selectedCategory: string | null;
  searchQuery: string;
  isLoading: boolean;
  /** Load application categories (with app_count) for the sidebar. */
  loadCategories: () => Promise<void>;
  /** Load applications filtered server-side by category + search query. */
  loadApps: (category?: string) => Promise<void>;
  /** Load a single application by slug (detail/runner pages). */
  loadApp: (slug: string) => Promise<AppItem | null>;
  selectCategory: (slug: string | null) => void;
  setSearchQuery: (query: string) => void;
  /** Back to the inert state — called by resetSessionScopedState (P0-2). */
  reset: () => void;
}

/** Unwrap a DRF response that may be a plain array or a paginated { results } object. */
function unwrap<T>(response: any): T[] {
  return Array.isArray(response) ? response : (response?.results ?? []);
}

/** Map an API application object to the frontend AppItem shape (id = slug). */
function toAppItem(app: any): AppItem {
  return {
    id: app.slug ?? app.application_slug,
    applicationId: app.id ?? app.application_id,
    name: app.name ?? app.application_name,
    description: app.description ?? app.application_description,
    category: app.category_slug ?? app.category?.slug ?? '',
    icon: app.icon ?? app.application_icon,
    color: app.color ?? app.application_color,
    tags: app.tags ?? [],
    developer: app.developer,
    screenshots: app.screenshots,
    usage_count: app.usage_count,
    kind: app.kind,
    rendererKey: app.renderer_key,
    runtime: app.renderer_key ? app : undefined,
    canEdit: Boolean(app.can_edit),
  };
}

// React StrictMode intentionally mounts effects twice in development. Share
// concurrent detail requests so page effects do not consume API quota twice.
const inFlightAppRequests = new Map<string, Promise<AppItem | null>>();

function resetAppRequestRegistry(): void {
  inFlightAppRequests.clear();
}

export const useAppStore = create<AppState>((set, get) => ({
  apps: [],
  categories: [],
  selectedCategory: null,
  searchQuery: '',
  isLoading: false,

  reset: () => {
    resetAppRequestRegistry();
    set({
      apps: [],
      categories: [],
      selectedCategory: null,
      searchQuery: '',
      isLoading: false,
    });
  },

  loadCategories: async () => {
    const generation = captureSessionGeneration();
    try {
      const response = await api.get<any>('/apps/categories/');
      if (!sessionStillCurrent(generation)) return;
      const categories = unwrap<AppCategory>(response);
      set({ categories });
    } catch (error) {
      console.error('Failed to load app categories:', error);
    }
  },

  loadApps: async (category?: string) => {
    const generation = captureSessionGeneration();
    try {
      set({ isLoading: true });
      const { searchQuery } = get();
      const params: any = {};
      if (category) params.category = category;
      if (searchQuery) params.search = searchQuery;
      const response = await api.get<any>('/apps/', { params });
      if (!sessionStillCurrent(generation)) return;
      const apps = unwrap<any>(response).map(toAppItem);
      set({ apps });
    } catch (error) {
      console.error('Failed to load apps:', error);
    } finally {
      if (sessionStillCurrent(generation)) set({ isLoading: false });
    }
  },

  loadApp: (slug: string) => {
    const existing = inFlightAppRequests.get(slug);
    if (existing) return existing;

    const generation = captureSessionGeneration();
    const request = api.get<any>(`/apps/${slug}/`)
      .then((response) => {
        if (!sessionStillCurrent(generation)) return null;
        return toAppItem(response);
      })
      .catch((error) => {
        if (!sessionStillCurrent(generation)) return null;
        console.error('Failed to load app:', error);
        return null;
      })
      .finally(() => {
        if (inFlightAppRequests.get(slug) === request) {
          inFlightAppRequests.delete(slug);
        }
      });
    inFlightAppRequests.set(slug, request);
    return request;
  },

  selectCategory: (slug) => set({ selectedCategory: slug }),

  setSearchQuery: (query) => set({ searchQuery: query }),
}));

// The legacy app list is per-user (P0-2).
registerSessionReset(() => useAppStore.getState().reset());
