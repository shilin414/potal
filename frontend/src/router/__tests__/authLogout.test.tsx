// @vitest-environment jsdom

import React from 'react';
import { act } from 'react-dom/test-utils';
import { createRoot, type Root } from 'react-dom/client';
import {
  MemoryRouter,
  Route,
  Routes,
  useLocation,
} from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  post: vi.fn(),
  broadcastExplicitLogout: vi.fn(),
  explicitLogoutListener: undefined as undefined | (() => void),
}));

vi.mock('@/services/axios', () => ({
  default: { post: mocks.post },
}));

vi.mock('@/stores/authBoundary', () => ({
  broadcastExplicitLogout: mocks.broadcastExplicitLogout,
  subscribeExplicitLogout: (listener: () => void) => {
    mocks.explicitLogoutListener = listener;
    return () => { mocks.explicitLogoutListener = undefined; };
  },
}));

import { ProtectedRoute } from '@/router/guards';
import { useAuthStore } from '@/stores/useAuthStore';

const user = {
  id: '7', username: 'tester', email: '', role: 'user',
  created_at: '2026-09-18T00:00:00Z',
};

const roots: Root[] = [];

function LocationProbe() {
  const location = useLocation();
  return <div>{`${location.pathname}${location.search}`}</div>;
}

beforeEach(() => {
  mocks.post.mockReset();
  mocks.broadcastExplicitLogout.mockReset();
  useAuthStore.setState({
    user,
    isAuthenticated: true,
    isLoggingOut: false,
    explicitlyLoggedOut: false,
  });
});

afterEach(() => {
  while (roots.length) roots.pop()?.unmount();
  vi.restoreAllMocks();
});

describe('explicit logout lifecycle', () => {
  it('keeps the authenticated route mounted until the server revoke settles', async () => {
    let settle: ((value: unknown) => void) | undefined;
    mocks.post.mockReturnValueOnce(new Promise((resolve) => { settle = resolve; }));

    const pending = useAuthStore.getState().logout();

    expect(useAuthStore.getState()).toMatchObject({
      isAuthenticated: true,
      isLoggingOut: true,
      explicitlyLoggedOut: false,
    });
    expect(mocks.post).toHaveBeenCalledWith(
      '/auth/logout/', {}, { timeout: 5000 },
    );
    expect(mocks.broadcastExplicitLogout).toHaveBeenCalledTimes(1);

    settle?.({});
    await pending;
    expect(useAuthStore.getState()).toMatchObject({
      user: null,
      isAuthenticated: false,
      isLoggingOut: false,
      explicitlyLoggedOut: true,
    });
    expect(mocks.broadcastExplicitLogout).toHaveBeenCalledTimes(1);
  });

  it('still completes the explicit local logout after a transport failure', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    mocks.post.mockRejectedValueOnce(new Error('network down'));

    await useAuthStore.getState().logout();

    expect(useAuthStore.getState()).toMatchObject({
      user: null,
      isAuthenticated: false,
      isLoggingOut: false,
      explicitlyLoggedOut: true,
    });
  });

  it('applies an explicit logout broadcast received from another tab', () => {
    expect(mocks.explicitLogoutListener).toBeTypeOf('function');

    mocks.explicitLogoutListener?.();

    expect(useAuthStore.getState()).toMatchObject({
      user: null,
      isAuthenticated: false,
      isLoggingOut: false,
      explicitlyLoggedOut: true,
    });
    expect(mocks.broadcastExplicitLogout).not.toHaveBeenCalled();
  });

  it('routes explicit logout to the manual page, but session expiry to auto OAuth', async () => {
    const host = document.createElement('div');
    const root = createRoot(host);
    roots.push(root);
    useAuthStore.setState({
      user: null,
      isAuthenticated: false,
      isLoggingOut: false,
      explicitlyLoggedOut: true,
    });
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={['/private']}>
          <Routes>
            <Route
              path="/private"
              element={<ProtectedRoute><div>private</div></ProtectedRoute>}
            />
            <Route path="/login" element={<LocationProbe />} />
            <Route path="/auth/login" element={<LocationProbe />} />
          </Routes>
        </MemoryRouter>,
      );
    });
    expect(host.textContent).toBe('/auth/login?logged_out=1');

    root.unmount();
    roots.pop();
    const secondRoot = createRoot(host);
    roots.push(secondRoot);
    useAuthStore.setState({ explicitlyLoggedOut: false });
    await act(async () => {
      secondRoot.render(
        <MemoryRouter initialEntries={['/private']}>
          <Routes>
            <Route
              path="/private"
              element={<ProtectedRoute><div>private</div></ProtectedRoute>}
            />
            <Route path="/login" element={<LocationProbe />} />
            <Route path="/auth/login" element={<LocationProbe />} />
          </Routes>
        </MemoryRouter>,
      );
    });
    expect(host.textContent).toBe('/login');
  });

  it('keeps session-expiry clearAuth separate from explicit logout', () => {
    useAuthStore.setState({ explicitlyLoggedOut: true });

    useAuthStore.getState().clearAuth();

    expect(useAuthStore.getState()).toMatchObject({
      user: null,
      isAuthenticated: false,
      explicitlyLoggedOut: false,
    });
  });

  it('clears explicit-logout state when a new identity is accepted', () => {
    useAuthStore.setState({
      user: null,
      isAuthenticated: false,
      explicitlyLoggedOut: true,
    });

    useAuthStore.getState().acceptAuthenticatedUser(user);

    expect(useAuthStore.getState()).toMatchObject({
      user,
      isAuthenticated: true,
      explicitlyLoggedOut: false,
    });
  });
});