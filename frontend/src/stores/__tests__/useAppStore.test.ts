import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '@/services/api';
import { resetSessionScopedState } from '@/stores/resetSessionState';
import { useAppStore } from '../useAppStore';

vi.mock('@/services/api', () => ({
  api: {
    get: vi.fn(),
  },
}));

describe('useAppStore.loadApp', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    resetSessionScopedState();
  });

  it('shares concurrent requests for the same application', async () => {
    let resolveRequest: ((value: any) => void) | undefined;
    vi.mocked(api.get).mockImplementation(() => new Promise((resolve) => {
      resolveRequest = resolve;
    }));

    const first = useAppStore.getState().loadApp('wechat-article-writer');
    const second = useAppStore.getState().loadApp('wechat-article-writer');

    expect(api.get).toHaveBeenCalledTimes(1);
    resolveRequest?.({
      id: 1,
      slug: 'wechat-article-writer',
      name: '微信公众号文章生成',
    });

    await expect(first).resolves.toMatchObject({ id: 'wechat-article-writer' });
    await expect(second).resolves.toMatchObject({ id: 'wechat-article-writer' });
  });

  it('does not let a previous session in-flight promise swallow the next request', async () => {
    let resolveA: ((value: any) => void) | undefined;
    let resolveB: ((value: any) => void) | undefined;
    vi.mocked(api.get)
      .mockImplementationOnce(() => new Promise((resolve) => { resolveA = resolve; }))
      .mockImplementationOnce(() => new Promise((resolve) => { resolveB = resolve; }));

    const requestA = useAppStore.getState().loadApp('shared-slug');
    resetSessionScopedState();
    const requestB = useAppStore.getState().loadApp('shared-slug');

    expect(api.get).toHaveBeenCalledTimes(2);
    resolveA?.({ id: 1, slug: 'shared-slug', name: 'A 的应用' });
    resolveB?.({ id: 2, slug: 'shared-slug', name: 'B 的应用' });

    await expect(requestA).resolves.toBeNull();
    await expect(requestB).resolves.toMatchObject({
      id: 'shared-slug',
      applicationId: 2,
      name: 'B 的应用',
    });
  });
});
