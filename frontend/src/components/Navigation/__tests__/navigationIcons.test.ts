import { describe, expect, it } from 'vitest';
import { HomeOutlined } from '@ant-design/icons';
import {
  NAVIGATION_ICON_COMPONENTS,
  NAVIGATION_ICON_OPTIONS,
  getNavigationIconComponent,
  isNavigationIconId,
} from '@/components/Navigation/navigationIconComponents';

describe('navigation icon registry', () => {
  it('offers every registered outline icon to the settings UI', () => {
    expect(NAVIGATION_ICON_OPTIONS.map((option) => option.id).sort())
      .toEqual(Object.keys(NAVIGATION_ICON_COMPONENTS).sort());
  });

  it('recognizes registered ids and safely falls back for stale preferences', () => {
    expect(isNavigationIconId('rocket')).toBe(true);
    expect(isNavigationIconId('removed-icon')).toBe(false);
    expect(isNavigationIconId('constructor')).toBe(false);
    expect(getNavigationIconComponent('removed-icon')).toBe(HomeOutlined);
    expect(getNavigationIconComponent('constructor')).toBe(HomeOutlined);
  });
});
