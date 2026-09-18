// @vitest-environment jsdom

import { beforeEach, describe, expect, it } from 'vitest';
import { useNavigationPreferencesStore } from '@/stores/useNavigationPreferencesStore';

beforeEach(() => {
  localStorage.clear();
  useNavigationPreferencesStore.setState({ iconMode: 'outline', icons: {} });
});

describe('navigation preferences', () => {
  it('defaults to outline icons and persists mode plus per-item overrides', () => {
    const state = useNavigationPreferencesStore.getState();
    expect(state.iconMode).toBe('outline');
    expect(state.icons).toEqual({});

    state.setIconMode('emoji');
    useNavigationPreferencesStore.getState().setIcon('agents', 'star');

    expect(useNavigationPreferencesStore.getState()).toMatchObject({
      iconMode: 'emoji',
      icons: { agents: 'star' },
    });
    expect(localStorage.getItem('navigation-preferences')).toContain('star');
  });

  it('can reset icon overrides without changing the display mode', () => {
    useNavigationPreferencesStore.getState().setIconMode('hidden');
    useNavigationPreferencesStore.getState().setIcon('apps', 'tool');

    useNavigationPreferencesStore.getState().resetIcons();

    expect(useNavigationPreferencesStore.getState()).toMatchObject({
      iconMode: 'hidden',
      icons: {},
    });
  });

  it('can reset all navigation preferences to the upgrade defaults', () => {
    useNavigationPreferencesStore.getState().setIconMode('emoji');
    useNavigationPreferencesStore.getState().setIcon('root', 'rocket');

    useNavigationPreferencesStore.getState().reset();

    expect(useNavigationPreferencesStore.getState()).toMatchObject({
      iconMode: 'outline',
      icons: {},
    });
  });

  it('sanitizes stale persisted modes and icon ids during hydration', async () => {
    localStorage.setItem('navigation-preferences', JSON.stringify({
      state: {
        iconMode: 'future-mode',
        icons: { root: 'constructor', agents: 'star', unknown: 'rocket' },
      },
      version: 0,
    }));

    await useNavigationPreferencesStore.persist.rehydrate();

    expect(useNavigationPreferencesStore.getState()).toMatchObject({
      iconMode: 'outline',
      icons: { agents: 'star' },
    });
  });
});
