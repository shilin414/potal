/**
 * Contract tests for the session sync.
 *
 * The auth store is persisted, so a session restored from an older build has no
 * display_name/display_id — without this sync the chat would show a bare name
 * with no （user_id）.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ get: vi.fn() }));

vi.mock('@/services/axios', () => ({ default: { get: mocks.get } }));

import {
  bootstrapPersistedSession,
  bootstrapPersistedSessionOnce,
  fetchSessionUser,
  syncSessionUser,
} from '@/services/session';
import { useAuthStore } from '@/stores/useAuthStore';
import { resetSessionScopedState } from '@/stores/resetSessionState';

const setSignedIn = (user: Record<string, unknown> | null) => {
  useAuthStore.setState({
    user: user as any,
    isAuthenticated: user !== null,
  });
};

beforeEach(() => {
  mocks.get.mockReset();
  setSignedIn(null);
});

describe('fetchSessionUser', () => {
  it('returns the session payload', async () => {
    mocks.get.mockResolvedValue({ username: '吴志彬', display_id: '19127920' });

    await expect(fetchSessionUser()).resolves.toEqual({
      username: '吴志彬', display_id: '19127920',
    });
    expect(mocks.get).toHaveBeenCalledWith('/identity/session');
  });

  it('swallows transport errors (logged out / offline)', async () => {
    mocks.get.mockRejectedValue(new Error('401'));

    await expect(fetchSessionUser()).resolves.toBeNull();
  });
});

describe('syncSessionUser', () => {
  it('fills display identity into a stale persisted user', async () => {
    setSignedIn({ id: '30001', username: '吴志彬', email: '', role: 'creator', created_at: '' });
    mocks.get.mockResolvedValue({
      id: 30001,
      username: '吴志彬',
      display_name: '吴志彬',
      display_id: '19127920',
      avatar_url: 'https://example.test/a.png',
      auth_source: 'feishu',
      is_staff: false,
    });

    await syncSessionUser();

    const user = useAuthStore.getState().user as any;
    expect(user.display_name).toBe('吴志彬');
    expect(user.display_id).toBe('19127920');
    expect(user.avatar_url).toBe('https://example.test/a.png');
    expect(user.auth_source).toBe('feishu');
    expect(user.is_staff).toBe(false);
  });

  it('keeps the previous display values when the session omits them', async () => {
    setSignedIn({ id: '1', username: 'demo', display_name: 'demo', display_id: '3', created_at: '' });
    mocks.get.mockResolvedValue({ id: 1, username: 'demo', avatar_url: '' });

    await syncSessionUser();

    const user = useAuthStore.getState().user as any;
    expect(user.display_name).toBe('demo');
    expect(user.display_id).toBe('3');
  });

  it('does nothing when signed out', async () => {
    setSignedIn(null);

    await expect(syncSessionUser()).resolves.toBeNull();
    expect(mocks.get).not.toHaveBeenCalled();
  });

  it('does nothing when the session payload is unusable', async () => {
    setSignedIn({ id: '1', username: 'demo', display_id: '3', created_at: '' });
    mocks.get.mockResolvedValue({ id: 1 });

    await expect(syncSessionUser()).resolves.toBeNull();
    expect((useAuthStore.getState().user as any).display_id).toBe('3');
  });

  it('drops an identity response that belongs to the previous account', async () => {
    setSignedIn({ id: 'A', username: 'A', display_name: 'A', created_at: '' });
    let release: (value: unknown) => void = () => {};
    mocks.get.mockReturnValueOnce(new Promise((resolve) => { release = resolve; }));

    const pending = syncSessionUser();
    // A logs out, B logs in while A's /identity/session is still travelling.
    resetSessionScopedState();
    setSignedIn({ id: 'B', username: 'B', display_name: 'B', avatar_url: 'b.png', created_at: '' });
    release({
      id: 'A', username: 'A', display_name: 'A-renamed',
      avatar_url: 'a.png', is_staff: true,
    });
    await expect(pending).resolves.toBeNull();

    const user = useAuthStore.getState().user as any;
    expect(user.id).toBe('B');
    expect(user.display_name).toBe('B');
    expect(user.avatar_url).toBe('b.png');
    expect(user.is_staff).not.toBe(true);
  });

  it('drops a same-epoch payload that names a different user id', async () => {
    setSignedIn({ id: 'B', username: 'B', display_name: 'B', created_at: '' });
    mocks.get.mockResolvedValue({ id: 'A', username: 'A', display_name: 'A' });

    await expect(syncSessionUser()).resolves.toBeNull();
    expect((useAuthStore.getState().user as any).display_name).toBe('B');
  });
});

describe('bootstrapPersistedSession', () => {
  it('removes cached staff authority before the session request resolves', async () => {
    setSignedIn({
      id: '3', username: 'demo', display_name: 'demo',
      is_staff: true, created_at: '',
    });
    let release: (value: unknown) => void = () => {};
    mocks.get.mockReturnValueOnce(new Promise((resolve) => { release = resolve; }));

    const pending = bootstrapPersistedSession();

    expect((useAuthStore.getState().user as any).is_staff).toBe(false);
    release({
      id: 3, username: 'demo', display_name: 'demo', is_staff: true,
    });
    await pending;
    expect((useAuthStore.getState().user as any).is_staff).toBe(true);
  });

  it('replaces a cached administrator with the cookie session identity', async () => {
    setSignedIn({
      id: '3', username: 'demo', email: 'demo@local.test', role: 'admin',
      display_name: 'demo', is_staff: true, created_at: 'old',
    });
    mocks.get.mockResolvedValueOnce({
      id: 1, username: 'system', role: 'creator', display_name: 'System User',
      display_id: '1', auth_source: 'feishu', is_staff: false,
    });

    await bootstrapPersistedSession();

    expect(useAuthStore.getState().user).toMatchObject({
      id: '1', username: 'system', email: '', role: 'creator',
      display_name: 'System User', is_staff: false, created_at: '',
    });
  });

  it('clears auth and private state when the server identity cannot be verified', async () => {
    setSignedIn({
      id: '3', username: 'demo', display_name: 'demo',
      is_staff: true, created_at: '',
    });
    mocks.get.mockRejectedValueOnce(new Error('network down'));

    await bootstrapPersistedSession();

    expect(useAuthStore.getState()).toMatchObject({
      user: null,
      isAuthenticated: false,
    });
  });

  it('shares one initial request across duplicate StrictMode effects', async () => {
    setSignedIn({
      id: '3', username: 'demo', display_name: 'demo',
      is_staff: true, created_at: '',
    });
    let release: (value: unknown) => void = () => {};
    mocks.get.mockReturnValueOnce(new Promise((resolve) => { release = resolve; }));

    const first = bootstrapPersistedSessionOnce();
    const second = bootstrapPersistedSessionOnce();

    expect(mocks.get).toHaveBeenCalledTimes(1);
    release({ id: 3, username: 'demo', is_staff: true });
    await Promise.all([first, second]);
    expect((useAuthStore.getState().user as any).is_staff).toBe(true);
  });

  it('ignores a superseded administrator response after a newer check fails', async () => {
    setSignedIn({
      id: '3', username: 'demo', display_name: 'demo',
      is_staff: true, created_at: '',
    });
    let releaseOld: (value: unknown) => void = () => {};
    mocks.get
      .mockReturnValueOnce(new Promise((resolve) => { releaseOld = resolve; }))
      .mockRejectedValueOnce(new Error('new request failed'));

    const oldRequest = bootstrapPersistedSession();
    const newRequest = bootstrapPersistedSession();
    await newRequest;
    releaseOld({ id: 3, username: 'demo', is_staff: true });
    await oldRequest;

    expect(useAuthStore.getState()).toMatchObject({
      user: null,
      isAuthenticated: false,
    });
  });
});
