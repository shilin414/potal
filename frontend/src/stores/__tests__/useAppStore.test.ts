import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '@/services/api';
import { useAppStore } from '../useAppStore';

vi.mock('@/services/api', () => ({
  api: {
    get: vi.fn(),
  },
}));

describe('useAppStore.loadApp', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
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
});
