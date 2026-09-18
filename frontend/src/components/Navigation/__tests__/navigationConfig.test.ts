import { describe, expect, it } from 'vitest';
import {
  NAVIGATION_ITEMS,
  getVisibleNavigationItems,
  isNavigationItemActive,
} from '@/components/Navigation/navigationConfig';

describe('navigationConfig', () => {
  it('keeps the product navigation set and desktop/mobile root labels centralized', () => {
    expect(NAVIGATION_ITEMS.map((item) => item.id)).toEqual([
      'root', 'agents', 'apps', 'schedules', 'enterprise',
    ]);
    expect(NAVIGATION_ITEMS[0]).toMatchObject({
      desktopLabel: '对话',
      mobileLabel: '首页',
      defaultIcon: 'message',
      mobileDefaultIcon: 'home',
    });
    expect(NAVIGATION_ITEMS.some((item) => (
      ['skills', 'templates', 'workflows'] as string[]
    ).includes(item.id))).toBe(false);
  });

  it.each([
    ['root', '/', true],
    ['root', '/chat/main', true],
    ['root', '/apps', false],
    ['agents', '/agents', true],
    ['agents', '/agents/42', true],
    ['apps', '/apps', true],
    ['apps', '/apps/42', true],
    ['apps', '/app/customer-service', true],
    ['apps', '/workflow/monthly-report', true],
    ['schedules', '/schedules/7', true],
    ['enterprise', '/enterprise/directory', true],
  ] as const)('matches %s against %s', (itemId, pathname, expected) => {
    expect(isNavigationItemActive(itemId, pathname)).toBe(expected);
  });

  it('keeps enterprise visibility in the shared navigation model', () => {
    expect(getVisibleNavigationItems({ isStaff: false }).map((item) => item.id))
      .not.toContain('enterprise');
    expect(getVisibleNavigationItems({ isStaff: true }).map((item) => item.id))
      .toContain('enterprise');
  });
});
