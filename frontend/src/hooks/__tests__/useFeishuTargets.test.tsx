/**
 * useFeishuTargets — 飞书目标远程搜索回归（五次复审 P1-3 / P2-7 / P2-8）。
 *
 *   · user：空 query 不发请求（provider 一页只有 20 条，空查询拿到的是
 *     「全公司任意前 20 人」）；有 query 才服务端搜索；
 *   · chat：query 直发后端过滤，空 query = 全量群聊；
 *   · 请求代际：旧查询晚到的响应整体丢弃（ForwardModal 旧实现曾让慢的
 *     旧查询覆盖新查询）；
 *   · ERROR ≠ EMPTY：失败暴露 error + errorStatus，refresh 可重试；
 *   · enabled / sessionKey 变化：立即作废在途请求并清空会话状态。
 * @vitest-environment jsdom
 */
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';
import { useFeishuTargets, type UseFeishuTargetsOptions, type UseFeishuTargetsResult } from '../useFeishuTargets';
import { fetchFeishuTargets, type FeishuForwardTarget } from '@/services/shareApi';

vi.mock('@/services/shareApi', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  fetchFeishuTargets: vi.fn(),
}));

const mockFetch = vi.mocked(fetchFeishuTargets);

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const target = (id: string, name: string, type: 'user' | 'chat' = 'user'): FeishuForwardTarget => ({
  id, name, avatar_url: '', target_type: type,
});

const flush = async (ms = 0) => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms));
  });
};

/** Deferred promise — 由测试决定响应何时落地。 */
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

interface Probe { current: UseFeishuTargetsResult | null }

let host: HTMLElement;
let root: Root;
let latest: Probe;
let options: UseFeishuTargetsOptions;

function ProbeComponent() {
  latest.current = useFeishuTargets(options);
  return null;
}

function rerender() {
  return act(async () => { root.render(<ProbeComponent />); });
}

beforeEach(() => {
  mockFetch.mockReset();
  latest = { current: null };
  options = { type: 'chat', enabled: true, query: '' };
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => { root.unmount(); });
  host.remove();
});

describe('useFeishuTargets — 搜索契约（五次复审 P1-3）', () => {
  it('user: an EMPTY query never fires — the provider page of 20 is not "any 20 coworkers"', async () => {
    mockFetch.mockResolvedValue([target('u1', '张三')]);
    options = { type: 'user', enabled: true, query: '' };
    await rerender();
    await flush(350);
    expect(mockFetch).not.toHaveBeenCalled();
    expect(latest.current!.items).toEqual([]);
    expect(latest.current!.loading).toBe(false);
  });

  it('user: typing a name goes to the SERVER — the 21st coworker becomes findable', async () => {
    mockFetch.mockResolvedValue([target('u21', '第二十一人')]);
    options = { type: 'user', enabled: true, query: '' };
    await rerender();
    await flush(10);

    options = { ...options, query: '二十' };
    await rerender();
    await flush(350); // 300ms 防抖到期
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(mockFetch).toHaveBeenLastCalledWith('user', '二十');
    expect(latest.current!.items.map((t) => t.name)).toEqual(['第二十一人']);
  });

  it('chat: an empty query loads the full list; a term goes to the server filter', async () => {
    mockFetch.mockResolvedValue([target('c1', '销售群', 'chat')]);
    options = { type: 'chat', enabled: true, query: '' };
    await rerender();
    await flush(10);
    expect(mockFetch).toHaveBeenCalledWith('chat', undefined);

    options = { ...options, query: '销售' };
    await rerender();
    await flush(350);
    expect(mockFetch).toHaveBeenLastCalledWith('chat', '销售');
  });
});

describe('useFeishuTargets — 请求代际（五次复审 P2-8 / §29）', () => {
  it('a slow OLD query can never overwrite the NEW one', async () => {
    // q1=张 请求 A 在途；用户继续输入 q2=张三 请求 B；B 先返回、A 后返回
    // —— 最终列表只能是 B 的结果（旧实现里 A 会覆盖 B）。
    const a = deferred<FeishuForwardTarget[]>();
    const b = deferred<FeishuForwardTarget[]>();
    mockFetch
      .mockImplementationOnce(() => a.promise)
      .mockImplementationOnce(() => b.promise);
    options = { type: 'user', enabled: true, query: '张' };
    await rerender();
    await flush(350); // 防抖到期 → 请求 A 在途
    expect(latest.current!.loading).toBe(true);

    options = { ...options, query: '张三' };
    await rerender();
    await flush(350); // 新防抖到期 → 请求 B 在途

    // B 先返回：B 的结果落地。
    await act(async () => { b.resolve([target('u9', '张三本人')]); });
    await flush(10);
    expect(latest.current!.items.map((t) => t.name)).toEqual(['张三本人']);
    expect(latest.current!.loading).toBe(false);

    // A 后返回：整体丢弃，不得覆盖 B。
    await act(async () => { a.resolve([target('u1', '张'), target('u2', '张二号')]); });
    await flush(10);
    expect(latest.current!.items.map((t) => t.name)).toEqual(['张三本人']);
  });

  it('clearing the query invalidates an in-flight search immediately', async () => {
    const a = deferred<FeishuForwardTarget[]>();
    mockFetch.mockImplementationOnce(() => a.promise);
    options = { type: 'user', enabled: true, query: '张' };
    await rerender();
    await flush(350);
    expect(latest.current!.loading).toBe(true);

    // 用户删空搜索词：在途响应作废，不落任何 items。
    options = { ...options, query: '' };
    await rerender();
    await flush(10);
    expect(latest.current!.loading).toBe(false);

    await act(async () => { a.resolve([target('u1', '张')]); });
    await flush(10);
    expect(latest.current!.items).toEqual([]);
  });
});

describe('useFeishuTargets — ERROR ≠ EMPTY（五次复审 P2-7）', () => {
  it('a failure surfaces error + errorStatus instead of a silent empty list', async () => {
    mockFetch.mockRejectedValue({
      response: { status: 502, data: { error: '获取群聊列表失败' } },
    });
    options = { type: 'chat', enabled: true, query: '' };
    await rerender();
    await flush(10);

    expect(latest.current!.error).toBe('获取群聊列表失败');
    expect(latest.current!.errorStatus).toBe(502);
    expect(latest.current!.items).toEqual([]);
  });

  it('a 403 surfaces its status so the host can offer re-authorization', async () => {
    mockFetch.mockRejectedValue({
      response: { status: 403, data: { detail: '飞书权限不足' } },
    });
    options = { type: 'user', enabled: true, query: '张' };
    await rerender();
    await flush(350);

    expect(latest.current!.error).toBe('飞书权限不足');
    expect(latest.current!.errorStatus).toBe(403);
  });

  it('refresh retries with the CURRENT query and clears the error on success', async () => {
    mockFetch
      .mockRejectedValueOnce(new Error('network down'))
      .mockResolvedValueOnce([target('u1', '张三')]);
    options = { type: 'user', enabled: true, query: '张三' };
    await rerender();
    await flush(350);
    expect(latest.current!.error).toBeTruthy();

    await act(async () => { await latest.current!.refresh(); });
    await flush(10);
    expect(latest.current!.error).toBeNull();
    expect(latest.current!.items.map((t) => t.name)).toEqual(['张三']);
  });
});

describe('useFeishuTargets — 会话与关闭重置（五次复审 §37–§39）', () => {
  it('disabling the surface invalidates in-flight requests and clears session state', async () => {
    const a = deferred<FeishuForwardTarget[]>();
    mockFetch.mockImplementationOnce(() => a.promise);
    options = { type: 'chat', enabled: true, query: '' };
    await rerender();
    await flush(10);
    expect(latest.current!.loading).toBe(true);

    options = { ...options, enabled: false };
    await rerender();
    await flush(10);
    expect(latest.current!.loading).toBe(false);

    // 迟到的响应不得落地。
    await act(async () => { a.resolve([target('c1', '群', 'chat')]); });
    await flush(10);
    expect(latest.current!.items).toEqual([]);
  });

  it('a sessionKey change is a NEW session: immediate reset, no stale-q request, no stale items', async () => {
    // 会话 A：搜索「张伟」成功。
    mockFetch.mockResolvedValue([target('u1', '张伟')]);
    options = { type: 'user', enabled: true, query: '张伟', sessionKey: 'a' };
    await rerender();
    await flush(350);
    expect(latest.current!.items.map((t) => t.name)).toEqual(['张伟']);

    // 关闭 A（enabled=false + sessionKey → null）：query 同步清空，无新请求。
    mockFetch.mockClear();
    options = { ...options, enabled: false, sessionKey: null };
    await rerender();
    await flush(10);
    expect(latest.current!.items).toEqual([]);

    // 立即重开会话 B（不等防抖）：首屏请求必须 q=undefined（空词）。
    mockFetch.mockResolvedValue([target('c1', '默认群', 'chat')]);
    options = { type: 'chat', enabled: true, query: '', sessionKey: 'b' };
    await rerender();
    await flush(10);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(mockFetch).toHaveBeenLastCalledWith('chat', undefined);
  });
});
