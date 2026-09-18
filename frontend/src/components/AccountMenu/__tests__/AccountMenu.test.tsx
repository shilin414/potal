// @vitest-environment jsdom

import React, { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import AccountMenu from '@/components/AccountMenu/AccountMenu';
import { useAuthStore } from '@/stores/useAuthStore';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];

beforeEach(() => {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: false, media: query, onchange: null,
      addListener: vi.fn(), removeListener: vi.fn(),
      addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
    })),
  });
  useAuthStore.setState({
    user: {
      id: '7', username: 'tester', email: '', role: 'user',
      display_name: '测试用户', display_id: 'E-007',
      created_at: '2026-09-18T00:00:00Z',
    },
    isAuthenticated: true,
    isLoggingOut: false,
    explicitlyLoggedOut: false,
  });
});

afterEach(() => {
  while (roots.length) roots.pop()?.unmount();
  document.body.innerHTML = '';
});

describe('mobile account menu', () => {
  it('shows reachable account identity and logout action', async () => {
    const host = document.createElement('div');
    const root = createRoot(host);
    roots.push(root);

    await act(async () => {
      root.render(
        <MemoryRouter>
          <AccountMenu variant="panel" />
        </MemoryRouter>,
      );
    });

    expect(host.textContent).toContain('测试用户');
    expect(host.textContent).toContain('E-007');
    expect(host.textContent).toContain('界面设置');
    expect(host.textContent).toContain('退出登录');
  });

  it('disables repeated logout while server revocation is in flight', async () => {
    useAuthStore.setState({ isLoggingOut: true });
    const host = document.createElement('div');
    const root = createRoot(host);
    roots.push(root);

    await act(async () => {
      root.render(
        <MemoryRouter>
          <AccountMenu variant="panel" />
        </MemoryRouter>,
      );
    });

    const button = Array.from(host.querySelectorAll('button'))
      .find((item) => item.textContent?.includes('退出'));
    expect(button?.disabled).toBe(true);
    expect(host.textContent).toContain('退出中');
  });
});


describe('desktop account menu', () => {
  it('uses a focusable click trigger so keyboard users can reach settings', async () => {
    const host = document.createElement('div');
    document.body.appendChild(host);
    const root = createRoot(host);
    roots.push(root);

    await act(async () => {
      root.render(
        <MemoryRouter>
          <AccountMenu />
        </MemoryRouter>,
      );
    });

    const trigger = host.querySelector<HTMLButtonElement>('.header-user');
    expect(trigger?.tagName).toBe('BUTTON');
    trigger?.focus();
    expect(document.activeElement).toBe(trigger);

    await act(async () => {
      trigger?.click();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(document.body.textContent).toContain('导航与外观');
  });
});
