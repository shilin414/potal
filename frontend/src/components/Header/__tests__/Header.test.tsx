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

vi.mock('@/components/AccountMenu/AccountMenu', () => ({
  default: () => <div data-testid="account-menu" />,
}));
vi.mock('@/components/Theme', () => ({
  ThemeToggle: () => <div data-testid="theme-toggle" />,
}));

import Header from '@/components/Header/Header';
import { useAuthStore } from '@/stores/useAuthStore';
import { useNavigationPreferencesStore } from '@/stores/useNavigationPreferencesStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Array<{ host: HTMLElement; root: Root }> = [];

const LocationProbe = () => {
  const location = useLocation();
  return <output data-testid="location">{location.pathname}</output>;
};

async function mountHeader(initialPath: string) {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  roots.push({ host, root });
  await act(async () => {
    root.render(
      <MemoryRouter initialEntries={[initialPath]}>
        <Routes>
          <Route path="*" element={<><Header /><LocationProbe /></>} />
        </Routes>
      </MemoryRouter>,
    );
  });
  return host;
}

function navButton(host: HTMLElement, label: string): HTMLButtonElement {
  const button = Array.from(host.querySelectorAll<HTMLButtonElement>('.header-nav-item'))
    .find((item) => item.textContent?.includes(label));
  if (!button) throw new Error(`missing navigation button: ${label}`);
  return button;
}

beforeEach(() => {
  useAuthStore.setState({
    user: {
      id: '1', username: 'tester', email: '', role: 'user',
      display_name: '测试用户', created_at: '', is_staff: true,
    },
    isAuthenticated: true,
  });
  useNavigationPreferencesStore.setState({ iconMode: 'outline', icons: {} });
  useWorkspaceBootstrapStore.setState({
    defaultApplication: { id: 9, slug: 'main-agent', name: '主智能体' } as never,
    load: vi.fn().mockResolvedValue(undefined),
  });
});

afterEach(async () => {
  while (roots.length) {
    const { host, root } = roots.pop()!;
    await act(async () => root.unmount());
    host.remove();
  }
});

describe('Header navigation', () => {
  it('marks chat and fixed-app routes under their shared navigation entries', async () => {
    const chat = await mountHeader('/chat/customer-service');
    expect(navButton(chat, '首页').getAttribute('aria-current')).toBe('page');
    await act(async () => roots.pop()!.root.unmount());
    chat.remove();

    const app = await mountHeader('/app/monthly-report');
    expect(navButton(app, '应用').getAttribute('aria-current')).toBe('page');
  });

  it('opens the Workbench home from the 首页 navigation item', async () => {
    const host = await mountHeader('/agents');

    await act(async () => navButton(host, '首页').click());

    expect(host.querySelector('[data-testid="location"]')?.textContent)
      .toBe('/');
  });

  it('renders accessible buttons and responds to all three icon modes', async () => {
    const outline = await mountHeader('/');
    expect(navButton(outline, '首页').tagName).toBe('BUTTON');
    expect(navButton(outline, '首页').querySelector('.header-nav-icon')).toBeTruthy();
    await act(async () => roots.pop()!.root.unmount());
    outline.remove();

    useNavigationPreferencesStore.setState({ iconMode: 'emoji' });
    const emoji = await mountHeader('/');
    expect(navButton(emoji, '首页').textContent).toContain('💬');
    await act(async () => roots.pop()!.root.unmount());
    emoji.remove();

    useNavigationPreferencesStore.setState({ iconMode: 'hidden' });
    const hidden = await mountHeader('/');
    expect(navButton(hidden, '首页').textContent).toBe('首页');
    expect(navButton(hidden, '首页').querySelector('.header-nav-icon')).toBeNull();
  });

  it('keeps enterprise navigation restricted to staff', async () => {
    useAuthStore.setState((state) => ({
      user: state.user ? { ...state.user, is_staff: false } : null,
    }));
    const host = await mountHeader('/');
    expect(host.textContent).not.toContain('企业控制台');
  });
});

