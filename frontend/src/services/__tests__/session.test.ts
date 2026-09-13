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

import { fetchSessionUser, syncSessionUser } from '@/services/session';
import { useAuthStore } from '@/stores/useAuthStore';

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
});
