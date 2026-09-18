export type NavigationItemId = 'root' | 'agents' | 'apps' | 'schedules' | 'enterprise';

export interface NavigationContext {
  isStaff: boolean;
}

export interface NavigationItem {
  id: NavigationItemId;
  path: string;
  desktopLabel: string;
  mobileLabel: string;
  defaultIcon: import('./navigationIconComponents').NavigationIconId;
  mobileDefaultIcon?: import('./navigationIconComponents').NavigationIconId;
  emoji: string;
  visibility?: (context: NavigationContext) => boolean;
}

export const NAVIGATION_ITEMS: readonly NavigationItem[] = [
  {
    id: 'root',
    path: '/',
    desktopLabel: '对话',
    mobileLabel: '首页',
    defaultIcon: 'message',
    mobileDefaultIcon: 'home',
    emoji: '💬',
  },
  {
    id: 'agents',
    path: '/agents',
    desktopLabel: '智能体',
    mobileLabel: '智能体',
    defaultIcon: 'robot',
    emoji: '🤖',
  },
  {
    id: 'apps',
    path: '/apps',
    desktopLabel: '应用',
    mobileLabel: '应用',
    defaultIcon: 'apps',
    emoji: '🧩',
  },
  {
    id: 'schedules',
    path: '/schedules',
    desktopLabel: '定时任务',
    mobileLabel: '定时任务',
    defaultIcon: 'clock',
    emoji: '⏰',
  },
  {
    id: 'enterprise',
    path: '/enterprise',
    desktopLabel: '企业控制台',
    mobileLabel: '企业控制台',
    defaultIcon: 'building',
    emoji: '🏢',
    visibility: ({ isStaff }) => isStaff,
  },
] as const;

export function getVisibleNavigationItems(context: NavigationContext): NavigationItem[] {
  return NAVIGATION_ITEMS.filter((item) => item.visibility?.(context) ?? true);
}

export function isNavigationItemActive(
  itemId: NavigationItemId,
  pathname: string,
): boolean {
  switch (itemId) {
    case 'root':
      return pathname === '/' || pathname.startsWith('/chat/');
    case 'agents':
      return pathname === '/agents' || pathname.startsWith('/agents/');
    case 'apps':
      return pathname === '/apps'
        || pathname.startsWith('/apps/')
        || pathname.startsWith('/app/')
        || pathname.startsWith('/workflow/');
    case 'schedules':
      return pathname === '/schedules' || pathname.startsWith('/schedules/');
    case 'enterprise':
      return pathname === '/enterprise' || pathname.startsWith('/enterprise/');
  }
}
