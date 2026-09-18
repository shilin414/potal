// @vitest-environment jsdom

import React, { StrictMode } from 'react';
import { act } from 'react-dom/test-utils';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
}));

vi.mock('@/services/axios', () => ({
  default: { get: mocks.get, post: vi.fn() },
}));
vi.mock('@/router', () => ({ default: {} }));
vi.mock('react-router-dom', () => ({
  RouterProvider: () => <div>private-router</div>,
}));

import App from '@/App';
import { useAuthStore } from '@/stores/useAuthStore';
import {
  useWorkspaceStore,
  workspaceStateOf,
} from '@/stores/useWorkspaceStore';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean })
  .IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];
const admin = {
  id: '3', username: 'demo', email: '', role: 'admin',
  display_name: 'demo', is_staff: true, created_at: '',
};

beforeEach(() => {
  mocks.get.mockReset();
  localStorage.clear();
  useWorkspaceStore.getState().clearAll();
  useAuthStore.setState({
    user: admin,
    isAuthenticated: true,
    isLoggingOut: false,
    explicitlyLoggedOut: false,
  });
});

afterEach(async () => {
  while (roots.length) {
    const root = roots.pop();
    await act(async () => { root?.unmount(); });
  }
  vi.restoreAllMocks();
});

async function renderApp(): Promise<{ host: HTMLDivElement; root: Root }> {
  const host = document.createElement('div');
  const root = createRoot(host);
  roots.push(root);
  await act(async () => {
    root.render(<StrictMode><App /></StrictMode>);
    await Promise.resolve();
  });
  return { host, root };
}

describe('App session boundary', () => {
  it('coalesces StrictMode bootstrap and blocks the router until verified', async () => {
    let release: (value: unknown) => void = () => {};
    mocks.get.mockReturnValueOnce(new Promise((resolve) => { release = resolve; }));
    const { host } = await renderApp();

    expect(mocks.get).toHaveBeenCalledTimes(1);
    expect(host.textContent).toContain('正在验证登录状态');
    expect(host.textContent).not.toContain('private-router');

    await act(async () => {
      release({ id: 3, username: 'demo', is_staff: true });
      await Promise.resolve();
    });

    expect(host.textContent).toContain('private-router');
    expect((useAuthStore.getState().user as any).is_staff).toBe(true);
  });

  it('revalidates an active tab when another tab changes identity', async () => {
    mocks.get
      .mockResolvedValueOnce({ id: 3, username: 'demo', is_staff: true })
      .mockResolvedValueOnce({
        id: 1, username: 'system', role: 'creator',
        display_name: 'System User', is_staff: false,
      });
    const { host } = await renderApp();
    expect(host.textContent).toContain('private-router');
    useWorkspaceStore.getState().setDraft(42, 'previous user draft');

    await act(async () => {
      window.dispatchEvent(new StorageEvent('storage', {
        key: 'studio-auth-event',
        newValue: JSON.stringify({
          type: 'identity-changed', nonce: 'other-tab-login', user_id: '1',
        }),
      }));
      await Promise.resolve();
    });

    expect(mocks.get).toHaveBeenCalledTimes(2);
    expect(useAuthStore.getState().user).toMatchObject({
      id: '1', username: 'system', is_staff: false,
    });
    expect(workspaceStateOf(useWorkspaceStore.getState().workspaces, 42).draft)
      .toBe('');
    expect(host.textContent).toContain('private-router');
  });

  it('refreshes the staff flag when an existing tab regains focus', async () => {
    mocks.get
      .mockResolvedValueOnce({ id: 3, username: 'demo', is_staff: true })
      .mockResolvedValueOnce({ id: 3, username: 'demo', is_staff: false });
    await renderApp();
    expect((useAuthStore.getState().user as any).is_staff).toBe(true);

    await act(async () => {
      window.dispatchEvent(new Event('focus'));
      await Promise.resolve();
    });

    expect(mocks.get).toHaveBeenCalledTimes(2);
    expect((useAuthStore.getState().user as any).is_staff).toBe(false);
  });

  it('serializes paired visibility and focus refreshes with a trailing check', async () => {
    let releaseFirst: (value: unknown) => void = () => {};
    let releaseTrailing: (value: unknown) => void = () => {};
    mocks.get
      .mockResolvedValueOnce({ id: 3, username: 'demo', is_staff: true })
      .mockReturnValueOnce(new Promise((resolve) => { releaseFirst = resolve; }))
      .mockReturnValueOnce(new Promise((resolve) => { releaseTrailing = resolve; }));
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
    await renderApp();

    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'));
      window.dispatchEvent(new Event('focus'));
      await Promise.resolve();
    });
    expect(mocks.get).toHaveBeenCalledTimes(2);

    await act(async () => {
      releaseFirst({ id: 3, username: 'demo', is_staff: true });
      await Promise.resolve();
    });
    expect(mocks.get).toHaveBeenCalledTimes(3);

    await act(async () => {
      releaseTrailing({ id: 3, username: 'demo', is_staff: false });
      await Promise.resolve();
    });
    expect((useAuthStore.getState().user as any).is_staff).toBe(false);
  });

  it('ignores a rebroadcast for the identity already installed', async () => {
    mocks.get.mockResolvedValueOnce({ id: 3, username: 'demo', is_staff: true });
    const { host } = await renderApp();
    useWorkspaceStore.getState().setDraft(42, 'keep this draft');

    await act(async () => {
      window.dispatchEvent(new StorageEvent('storage', {
        key: 'studio-auth-event',
        newValue: JSON.stringify({
          type: 'identity-changed', nonce: 'same-user-rebroadcast', user_id: '3',
        }),
      }));
      await Promise.resolve();
    });

    expect(mocks.get).toHaveBeenCalledTimes(1);
    expect(workspaceStateOf(useWorkspaceStore.getState().workspaces, 42).draft)
      .toBe('keep this draft');
    expect(host.textContent).toContain('private-router');
  });
});
