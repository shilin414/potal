import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import type { NavigationItemId } from '@/components/Navigation/navigationConfig';
import {
  isNavigationIconId,
  type NavigationIconId,
} from '@/components/Navigation/navigationIconComponents';

export type NavigationIconMode = 'outline' | 'emoji' | 'hidden';
export type NavigationIconPreferences = Partial<Record<NavigationItemId, NavigationIconId>>;

interface NavigationPreferencesState {
  iconMode: NavigationIconMode;
  icons: NavigationIconPreferences;
  setIconMode: (iconMode: NavigationIconMode) => void;
  setIcon: (itemId: NavigationItemId, iconId: NavigationIconId) => void;
  resetIcons: () => void;
  reset: () => void;
}

const DEFAULT_ICON_MODE: NavigationIconMode = 'outline';

function isNavigationIconMode(value: unknown): value is NavigationIconMode {
  return value === 'outline' || value === 'emoji' || value === 'hidden';
}

function sanitizeIcons(value: unknown): NavigationIconPreferences {
  if (!value || typeof value !== 'object') return {};
  const allowedItemIds: NavigationItemId[] = [
    'root', 'agents', 'apps', 'schedules', 'enterprise',
  ];
  return Object.fromEntries(
    allowedItemIds.flatMap((itemId) => {
      const iconId = (value as Record<string, unknown>)[itemId];
      return isNavigationIconId(iconId) ? [[itemId, iconId]] : [];
    }),
  ) as NavigationIconPreferences;
}

export const useNavigationPreferencesStore = create<NavigationPreferencesState>()(
  persist(
    (set) => ({
      iconMode: DEFAULT_ICON_MODE,
      icons: {},
      setIconMode: (iconMode) => set({ iconMode }),
      setIcon: (itemId, iconId) => set((state) => ({
        icons: { ...state.icons, [itemId]: iconId },
      })),
      resetIcons: () => set({ icons: {} }),
      reset: () => set({ iconMode: DEFAULT_ICON_MODE, icons: {} }),
    }),
    {
      name: 'navigation-preferences',
      partialize: (state) => ({ iconMode: state.iconMode, icons: state.icons }),
      merge: (persisted, current) => {
        const saved = persisted as Partial<NavigationPreferencesState> | undefined;
        return {
          ...current,
          iconMode: isNavigationIconMode(saved?.iconMode)
            ? saved.iconMode
            : DEFAULT_ICON_MODE,
          icons: sanitizeIcons(saved?.icons),
        };
      },
    },
  ),
);
