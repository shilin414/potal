// @vitest-environment jsdom

import React, { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import {
  MemoryRouter,
  Route,
  Routes,
  useLocation,
} from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('antd', () => ({
  Button: ({ children, icon, onClick, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement> & { icon?: React.ReactNode }) => (
    <button type="button" onClick={onClick} {...props}>{icon}{children}</button>
  ),
  Drawer: ({ open, children, title, onClose }: {
    open: boolean;
    children: React.ReactNode;
    title?: React.ReactNode;
    onClose: () => void;
  }) => open ? (
    <aside data-testid="drawer">
      <div>{title}</div>
      <button type="button" onClick={onClose}>关闭抽屉</button>
      {children}
    </aside>
  ) : null,
}));
vi.mock('@/components/ConversationHistory/ConversationHistory', () => ({
  default: ({ onNewConversation }: { onNewConversation: () => void }) => (
    <button type="button" onClick={onNewConversation}>最近会话</button>
  ),
}));
vi.mock('@/components/AccountMenu/AccountMenu', () => ({
  default: () => <div>账号</div>,
}));
vi.mock('@/components/Mobile/MobileAgentSwitcher', () => ({
  default: () => <div>智能体切换</div>,
}));
vi.mock('@/components/Theme', () => ({
  ThemePicker: () => <div>主题</div>,
}));

import MobileAppShell from '@/shell/MobileAppShell';
import { useAuthStore } from '@/stores/useAuthStore';
import { useNavigationPreferencesStore } from '@/stores/useNavigationPreferencesStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Array<{ host: HTMLElement; root: Root }> = [];

const LocationProbe = () => {
  const location = useLocation();
  return <output data-testid="location">{location.pathname}</output>;
};

function buttonByText(text: string): HTMLButtonElement {
  const button = Array.from(document.querySelectorAll<HTMLButtonElement>('button'))
    .find((item) => item.textContent?.includes(text));
  if (!button) throw new Error(`missing button: ${text}`);
  return button;
}

async function mountShell(initialPath: string) {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  roots.push({ host, root });
  await act(async () => {
    root.render(
      <MemoryRouter initialEntries={[initialPath]}>
        <Routes>
          <Route
            path="*"
            element={(
              <>
                <MobileAppShell chrome={{ hideHeader: false, hideSidebar: false, padded: false }} />
                <LocationProbe />
              </>
            )}
          />
        </Routes>
      </MemoryRouter>,
    );
  });
}

beforeEach(() => {
  useWorkspaceStore.setState({ mobileNavOpen: false });
  useNavigationPreferencesStore.setState({ iconMode: 'outline', icons: {} });
  useAuthStore.setState({
    user: {
      id: '1', username: 'tester', email: '', role: 'user',
      display_name: '测试用户', created_at: '', is_staff: true,
    },
    isAuthenticated: true,
  });
});

afterEach(async () => {
  while (roots.length) {
    const { host, root } = roots.pop()!;
    await act(async () => root.unmount());
    host.remove();
  }
  document.body.innerHTML = '';
});

describe('MobileAppShell navigation regression', () => {
  it('opens the drawer, keeps supporting surfaces, marks chat active, and closes after navigation', async () => {
    await mountShell('/chat/main-agent');

    const menuButton = document.querySelector<HTMLButtonElement>('[aria-label="打开导航"]');
    if (!menuButton) throw new Error('missing mobile menu button');
    await act(async () => menuButton.click());
    const drawer = document.querySelector('[data-testid="drawer"]');
    expect(drawer).toBeTruthy();
    expect(drawer?.textContent).toContain('最近会话');
    expect(drawer?.textContent).toContain('主题');
    expect(drawer?.textContent).toContain('账号');

    const home = buttonByText('首页');
    expect(home.getAttribute('aria-current')).toBe('page');
    expect(home.querySelector('.mobile-shell__nav-icon')).toBeTruthy();

    await act(async () => buttonByText('应用').click());
    expect(document.querySelector('[data-testid="drawer"]')).toBeNull();
    expect(document.querySelector('[data-testid="location"]')?.textContent).toBe('/apps');
  });

  it('keeps 新任务 navigating to the mobile home route', async () => {
    await mountShell('/agents');
    await act(async () => buttonByText('新任务').click());
    expect(document.querySelector('[data-testid="location"]')?.textContent).toBe('/');
  });
});
