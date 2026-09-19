/**
 * useDirectoryUsers — disabled 废弃在途请求回归（三次复审 §43–§45）。
 *
 * 关闭 Picker / 切走 Users Tab 时，`enabled: false` 之前只挡「新」请求，
 * 在途响应返回后仍会 setItems/setError/setLoading —— 下次重开有机会短暂
 * 闪现上一次的搜索结果/错误。现在 disabled 时 bump request id，把在途
 * 响应整体作废。renderHook-style coverage via createRoot/act。
 * @vitest-environment jsdom
 */
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';
import { useDirectoryUsers, type UseDirectoryUsersOptions } from '../useDirectoryUsers';
import { enterpriseApi, type DirectoryUser } from '../../enterpriseApi';

vi.mock('../../enterpriseApi', () => ({
  enterpriseApi: {
    users: vi.fn(),
  },
}));

const mockUsers = vi.mocked(enterpriseApi.users);

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const user = (id: number, name: string): DirectoryUser => ({
  id, name, avatar_url: '', open_id: `o-${id}`, active_status: 1,
  is_resigned: false, local_user_id: null, is_active: true,
  departments: [{ id: 1, name: '财务部', is_primary: true }],
} as unknown as DirectoryUser);

const flush = async (ms = 0) => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms));
  });
};

interface Probe {
  current: ReturnType<typeof useDirectoryUsers> | null;
}

let host: HTMLElement;
let root: Root;
let latest: Probe;
let options: UseDirectoryUsersOptions;

function ProbeComponent() {
  latest.current = useDirectoryUsers(options);
  return null;
}

beforeEach(() => {
  mockUsers.mockReset();
  latest = { current: null };
  options = { query: '', enabled: true };
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => { root.unmount(); });
  host.remove();
});

describe('useDirectoryUsers — disabled invalidation (§43–§45)', () => {
  it('a response arriving AFTER the surface went inactive is ignored', async () => {
    let resolveUsers!: (page: { results: DirectoryUser[]; next_cursor: string | null }) => void;
    mockUsers.mockReturnValueOnce(new Promise((resolve) => {
      resolveUsers = resolve;
    }));
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.loading).toBe(true);

    // Close the surface while the request is still travelling.
    options = { ...options, enabled: false };
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.loading).toBe(false); // spinner stopped

    // The stale response lands now — it must be ignored entirely.
    await act(async () => {
      resolveUsers({ results: [user(1, '张三')], next_cursor: null });
    });
    await flush(10);
    expect(latest.current!.items).toEqual([]); // nothing landed
    expect(latest.current!.loading).toBe(false);
  });

  it('a failure arriving after the surface went inactive is ignored too', async () => {
    let rejectUsers!: (reason?: unknown) => void;
    mockUsers.mockReturnValueOnce(new Promise((_resolve, reject) => {
      rejectUsers = reject;
    }));
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    options = { ...options, enabled: false };
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    await act(async () => { rejectUsers(new Error('closed')); });
    await flush(10);
    expect(latest.current!.error).toBeNull(); // no stale error to flash on reopen
  });
});

describe('useDirectoryUsers — loadMore 代际失效（四次复审 P1-2）', () => {
  it('a NEW search releases the stale loadMore spinner immediately; the new set pages on its own', async () => {
    // A 的 loadMore 在途时新搜索开始：旧请求必须当场作废（spinner 立即
    // false，而不是等旧 HTTP 结束），新搜索能立刻翻下一页；旧请求晚到时
    // 既不能覆盖新数据，也不能关掉新 loadMore 的 spinner。
    let resolveStale!: (page: { results: DirectoryUser[]; next_cursor: string | null }) => void;
    let resolveB!: (page: { results: DirectoryUser[]; next_cursor: string | null }) => void;
    mockUsers
      .mockResolvedValueOnce({ results: [user(1, '张三')], next_cursor: 'c1' })
      .mockImplementationOnce(() => new Promise((res) => { resolveStale = res; }))
      .mockResolvedValueOnce({ results: [user(9, '王五')], next_cursor: 'c9' })
      .mockImplementationOnce(() => new Promise((res) => { resolveB = res; }));
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.hasMore).toBe(true);

    const stale = latest.current!.loadMore();
    await flush(10);
    expect(latest.current!.loadingMore).toBe(true);

    // 新搜索在 loadMore 在途时开始：bump request id + 新首页。
    options = { ...options, query: '王' };
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(350); // 300ms 防抖到期 + 新首页落地
    expect(latest.current!.loading).toBe(false);
    expect(latest.current!.loadingMore).toBe(false); // 旧 loadMore 已当场作废

    // 新搜索可以立即翻自己的下一页（旧请求仍在途）。
    const bMore = latest.current!.loadMore();
    await flush(10);
    expect(mockUsers).toHaveBeenLastCalledWith(expect.objectContaining({
      q: '王', cursor: 'c9',
    }));
    expect(latest.current!.loadingMore).toBe(true);  // 新搜索自己的 spinner

    // 旧响应此刻才返回：不得覆盖新数据、不得关掉新 spinner。
    await act(async () => {
      resolveStale({ results: [user(5, '李四')], next_cursor: null });
    });
    await act(async () => { await stale; });
    await flush(10);
    expect(latest.current!.items.map((u) => u.id)).toEqual([9]);
    expect(latest.current!.loadingMore).toBe(true);  // 新 spinner 仍在
    expect(latest.current!.error).toBeNull();

    // 新搜索的第二页落地，spinner 才复位。
    await act(async () => {
      resolveB({ results: [user(10, '王五分身')], next_cursor: null });
    });
    await act(async () => { await bMore; });
    await flush(10);
    expect(latest.current!.loadingMore).toBe(false);
    expect(latest.current!.items.map((u) => u.id)).toEqual([9, 10]);
  });
});

describe('useDirectoryUsers — 快速关闭重开（五次复审 §33–§38）', () => {
  it('close → IMMEDIATELY open: no stale-q request, no stale items, no stale error', async () => {
    // 旧实现的测试要等 350ms 才敢重开 —— 实际用户 50ms 后就会重开。那时
    // 防抖还没到期，重开的第一帧会带着上一次的 q 先发一次请求、闪一次旧
    // 结果。sessionKey + 关闭期间同步防抖后，重开的第一屏必须直接 q=''。
    mockUsers.mockResolvedValue({ results: [user(1, '张伟')], next_cursor: null });
    options = { query: '张伟', enabled: true, sessionKey: 'a' };
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(350); // A 会话搜索落地
    expect(latest.current!.items.map((u) => u.name)).toEqual(['张伟']);

    // 关闭 A（selected → null）：sessionKey 立即重置 + 防抖同步清空。
    options = { query: '张伟', enabled: false, sessionKey: null };
    await act(async () => { root.render(<ProbeComponent />); });
    // 注意：这里不等 350ms —— 关闭的下一帧立即重开。
    await flush(10);
    expect(latest.current!.items).toEqual([]);

    // 立即重开 B：首屏请求不得携带 A 遗留的 q。
    mockUsers.mockClear();
    mockUsers.mockResolvedValue({ results: [user(2, '李四')], next_cursor: null });
    options = { query: '', enabled: true, sessionKey: 'b' };
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    expect(mockUsers).toHaveBeenCalledTimes(1);
    expect(mockUsers).toHaveBeenLastCalledWith(
      expect.not.objectContaining({ q: '张伟' }),
    );
    expect(latest.current!.items.map((u) => u.name)).toEqual(['李四']);
    expect(latest.current!.error).toBeNull();
  });

  it('a stale error from the closed session never flashes on reopen', async () => {
    mockUsers.mockRejectedValueOnce(new Error('network down'));
    options = { query: '张伟', enabled: true, sessionKey: 'a' };
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(350);
    expect(latest.current!.error).toBe('加载人员失败');

    // 关闭 → 立即重开：旧会话的 error 已被 sessionKey 重置清掉。
    mockUsers.mockResolvedValue({ results: [user(2, '李四')], next_cursor: null });
    options = { query: '', enabled: true, sessionKey: 'b' };
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);
    expect(latest.current!.error).toBeNull();
    expect(latest.current!.items.map((u) => u.name)).toEqual(['李四']);
  });

  it('switching straight from A to B (no close in between) clears the old session in the same frame', async () => {
    // A→B 直接切换（enabled 保持 true）：sessionKey 变化即新会话 ——
    // 旧会话的 items 当帧清空，首屏请求直接 q=''（宿主 render-time 清了
    // query 的前提下），不会带着 A 的搜索词先请求一次。
    mockUsers.mockResolvedValue({ results: [user(1, '张伟')], next_cursor: null });
    options = { query: '张伟', enabled: true, sessionKey: 'a' };
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(350);
    expect(latest.current!.items.map((u) => u.name)).toEqual(['张伟']);

    mockUsers.mockClear();
    mockUsers.mockResolvedValue({ results: [user(9, '王五')], next_cursor: null });
    // 宿主在会话切换的同一帧清空了 query（AccessPage 的 render-time 重置）。
    options = { query: '', enabled: true, sessionKey: 'b' };
    await act(async () => { root.render(<ProbeComponent />); });
    await flush(10);

    expect(mockUsers).toHaveBeenCalledTimes(1);
    expect(mockUsers).toHaveBeenLastCalledWith(
      expect.not.objectContaining({ q: '张伟' }),
    );
    expect(latest.current!.items.map((u) => u.name)).toEqual(['王五']);
  });
});
