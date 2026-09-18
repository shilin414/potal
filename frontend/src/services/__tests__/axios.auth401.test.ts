import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  clearAuth: vi.fn(),
  navigateToSessionLogin: vi.fn(),
  state: { isLoggingOut: false, explicitlyLoggedOut: false },
}));

vi.mock('@/stores/useAuthStore', () => ({
  useAuthStore: {
    getState: () => ({
      isLoggingOut: mocks.state.isLoggingOut,
      explicitlyLoggedOut: mocks.state.explicitlyLoggedOut,
      clearAuth: mocks.clearAuth,
    }),
  },
}));

vi.mock('@/services/authRedirect', () => ({
  navigateToSessionLogin: mocks.navigateToSessionLogin,
}));

import axiosInstance from '@/services/axios';

function rejectWith(status: number) {
  axiosInstance.defaults.adapter = async (config) => Promise.reject({
    config,
    response: { status, data: { detail: 'nope' }, headers: {}, config },
    isAxiosError: true,
  });
}

beforeEach(() => {
  mocks.clearAuth.mockReset();
  mocks.navigateToSessionLogin.mockReset();
  mocks.state.isLoggingOut = false;
  mocks.state.explicitlyLoggedOut = false;
  rejectWith(401);
});

describe('axios 401 ownership', () => {
  it('returns an admin-login 401 to the form without global session handling', async () => {
    await expect(axiosInstance.post('/identity/admin/login', {
      username: 'admin', password: 'wrong',
    })).rejects.toBeTruthy();

    expect(mocks.clearAuth).not.toHaveBeenCalled();
    expect(mocks.navigateToSessionLogin).not.toHaveBeenCalled();
  });

  it('does not let an in-flight 401 steal the explicit logout state machine', async () => {
    mocks.state.isLoggingOut = true;

    await expect(axiosInstance.get('/v2/workspace/bootstrap')).rejects.toBeTruthy();

    expect(mocks.clearAuth).not.toHaveBeenCalled();
    expect(mocks.navigateToSessionLogin).not.toHaveBeenCalled();
  });

  it('ignores a late 401 after explicit logout has already completed', async () => {
    mocks.state.explicitlyLoggedOut = true;

    await expect(axiosInstance.get('/v2/workspace/bootstrap')).rejects.toBeTruthy();

    expect(mocks.clearAuth).not.toHaveBeenCalled();
    expect(mocks.navigateToSessionLogin).not.toHaveBeenCalled();
  });

  it('hands logout endpoint 401s back to logout itself', async () => {
    await expect(axiosInstance.post('/auth/logout/')).rejects.toBeTruthy();

    expect(mocks.clearAuth).not.toHaveBeenCalled();
    expect(mocks.navigateToSessionLogin).not.toHaveBeenCalled();
  });

  it('clears session state and starts automatic Feishu re-authentication for ordinary 401s', async () => {
    await expect(axiosInstance.get('/v2/workspace/bootstrap')).rejects.toBeTruthy();

    expect(mocks.clearAuth).toHaveBeenCalledTimes(1);
    expect(mocks.navigateToSessionLogin).toHaveBeenCalledTimes(1);
  });
});
