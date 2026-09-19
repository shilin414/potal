/**
 * useFeishuTargets — 飞书目标远程搜索回归（五次复审 P1-3 / P2-7 / P2-8，
 * 六次复审 P1-3 / P2-1 / P2-3）。
 *
 *   · user：空 query 不发请求（idle ≠ success(0 条)，六次复审 P2-3）；
 *     有 query 才服务端搜索，并按 cursor 续拉（六次复审 P1-3 —— 官方
 *     search/v1/user 支持 page_size 1-200 / page_token，旧的「一页 20
 *     条」让第 21+ 人永远选不到）；
 *   · chat：一个 picker 会话只请求一次全量（六次复审 P2-1）—— 后端已
 *     翻到 has_more=false，query 由 hook 在完整数据集上本地过滤，输入
 *     「运/运营/运营群」不再每敲一个字符就重扫一遍完整群列表；
 *   · 请求代际：旧查询晚到的响应（含续拉页）整体丢弃，不得 append 到
 *     新查询的结果（ForwardModal 旧实现曾让慢的旧查询覆盖新查询）；
 *   · ERROR ≠ EMPTY：失败暴露 error + errorStatus，refresh 可重试；
 *   · enabled / sessionKey 变化：立即作废在途请求并清空会话状态；
 *     chat 会话缓存跨 enabled 切换存续（切 tab 回来不重拉）。
 * @vitest-environment jsdom
 */
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';
import { useFeishuTargets, type UseFeishuTargetsOptions, type UseFeishuTargetsResult } from '../useFeishuTargets';
import { fetchFeishuTargets, type FeishuForwardTarget, type FeishuForwardTargetPage } from '@/services/shareApi';

vi.mock('@/services/shareApi', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  fetchFeishuTargets: vi.fn(),
}));

const mockFetch = vi.mocked(fetchFeishuTargets);

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const target = (id: string, name: string, type: 'user' | 'chat' = 'user'): FeishuForwardTarget => ({
  id, name, avatar_url: '', target_type: type,
});

/** 官方分页 envelope（六次复审 P1-3）：{items, next_cursor, has_more}。 */
const page = (
  items: FeishuForwardTarget[],
  opts?: { hasMore?: boolean; cursor?: string },
): FeishuForwardTargetPage => ({
  items,
  next_cursor: opts?.cursor ?? '',
  has_more: opts?.hasMore ?? false,
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

describe('useFeishuTargets — 搜索契约（五次复审 P1-3 / 六次复审 P2-3）', () => {
  it('user: an EMPTY query never fires — status is IDLE, not success(empty)', async () => {
    mockFetch.mockResolvedValue(page([target('u1', '张三')]));
    options = { type: 'user', enabled: true, query: '' };
    await rerender();
    await flush(350);
    expect(mockFetch).not.toHaveBeenCalled();
    expect(latest.current!.items).toEqual([]);
    expect(latest.current!.loading).toBe(false);
    // idle = 未参与查询（六次复审 P2-3）：绝不是「成功加载了 0 条」。
    expect(latest.current!.status).toBe('idle');
  });

  it('user: a zero-row SUCCESS page is success, not idle — 未请求 ≠ 请求成功但 0 条', async () => {
    mockFetch.mockResolvedValue(page([]));
    options = { type: 'user', enabled: true, query: '不存在的人' };
    await rerender();
    await flush(350);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(latest.current!.items).toEqual([]);
    expect(latest.current!.status).toBe('success');
  });

  it('user: typing a name goes to the SERVER — the 21st coworker becomes findable', async () => {
    mockFetch.mockResolvedValue(page([target('u21', '第二十一人')]));
    options = { type: 'user', enabled: true, query: '' };
    await rerender();
    await flush(10);

    options = { ...options, query: '二十' };
    await rerender();
    await flush(350); // 300ms 防抖到期
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(mockFetch).toHaveBeenLastCalledWith('user', '二十');
    expect(latest.current!.items.map((t) => t.name)).toEqual(['第二十一人']);
    expect(latest.current!.status).toBe('success');
  });

  it('chat: one full-list fetch per session — typing only filters LOCALLY', async () => {
    mockFetch.mockResolvedValue(page([
      target('c1', '运营群', 'chat'),
      target('c2', '技术群', 'chat'),
    ]));
    options = { type: 'chat', enabled: true, query: '' };
    await rerender();
    await flush(10);
    // 空 query = 全量（后端已翻到 has_more=false），不带搜索词。
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(mockFetch).toHaveBeenLastCalledWith('chat', undefined);

    // 逐字输入（六次复审 P2-1）：旧实现每敲一个字符都重扫一遍完整群列表。
    options = { ...options, query: '运' };
    await rerender();
    await flush(350);
    options = { ...options, query: '运营' };
    await rerender();
    await flush(350);
    options = { ...options, query: '运营群' };
    await rerender();
    await flush(350);
    expect(mockFetch).toHaveBeenCalledTimes(1);

    // 本地过滤发生在完整数据集上（区别于旧的「只拉前 100 再过滤」）。
    expect(latest.current!.items.map((t) => t.name)).toEqual(['运营群']);

    // 清空搜索词：回到全量，仍然是那一次请求。
    options = { ...options, query: '' };
    await rerender();
    await flush(10);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(latest.current!.items.map((t) => t.name)).toEqual(['运营群', '技术群']);
  });
});

describe('useFeishuTargets — user cursor 分页（六次复审 P1-3）', () => {
  it('loadMore appends the next page (provider page_token as cursor) and dedupes', async () => {
    mockFetch
      .mockResolvedValueOnce(page([target('u1', '张一'), target('u2', '张二')], { hasMore: true, cursor: 'p2' }))
      .mockResolvedValueOnce(page([target('u3', '张三'), target('u1', '张一(重复)')], { hasMore: false }));
    options = { type: 'user', enabled: true, query: '张' };
    await rerender();
    await flush(350);
    expect(latest.current!.hasMore).toBe(true);

    await act(async () => { await latest.current!.loadMore(); });
    await flush(10);
    // 第二页带 cursor 续拉，append 且按 id 去重。
    expect(mockFetch).toHaveBeenLastCalledWith('user', '张', 'p2');
    expect(latest.current!.items.map((t) => t.name)).toEqual(['张一', '张二', '张三']);
    expect(latest.current!.hasMore).toBe(false);
    expect(latest.current!.loadingMore).toBe(false);
  });

  it('a late page from an OLD query never appends to the NEW query', async () => {
    // query 张：page1 已落地，page2（张三/张四）在途；用户改搜李。
    const stalePage2 = deferred<FeishuForwardTargetPage>();
    mockFetch
      .mockResolvedValueOnce(page([target('u1', '张一')], { hasMore: true, cursor: 'p2' }))
      .mockImplementationOnce(() => stalePage2.promise)
      .mockResolvedValueOnce(page([target('l1', '李一')]));
    options = { type: 'user', enabled: true, query: '张' };
    await rerender();
    await flush(350);
    await act(async () => { void latest.current!.loadMore(); });
    await flush(10); // 张/page2 在途

    options = { ...options, query: '李' };
    await rerender();
    await flush(350); // 李 page1 落地
    expect(latest.current!.items.map((t) => t.name)).toEqual(['李一']);

    // 旧 张/page2 迟到：不得 append 到李的结果。
    await act(async () => { stalePage2.resolve(page([target('u3', '张三'), target('u4', '张四')])); });
    await flush(10);
    expect(latest.current!.items.map((t) => t.name)).toEqual(['李一']);
  });

  it('a query change while a loadMore is in flight resets loadingMore', async () => {
    // 张 page1 落地（hasMore），续拉 page2 在途；用户改搜李 —— 新首页
    // 作废在途续拉后，loadingMore 必须复位：其 finally 的双 seq 检查必
    // 不过、不会自己清，卡 true 会永久锁死该会话的续拉入口。
    const stalePage2 = deferred<FeishuForwardTargetPage>();
    mockFetch
      .mockResolvedValueOnce(page([target('u1', '张一')], { hasMore: true, cursor: 'p2' }))
      .mockImplementationOnce(() => stalePage2.promise)
      .mockResolvedValueOnce(page([target('l1', '李一')], { hasMore: true, cursor: 'l2' }));
    options = { type: 'user', enabled: true, query: '张' };
    await rerender();
    await flush(350);
    await act(async () => { void latest.current!.loadMore(); });
    await flush(10);
    expect(latest.current!.loadingMore).toBe(true);

    options = { ...options, query: '李' };
    await rerender();
    await flush(350); // 李 page1 落地 + 作废在途续拉
    expect(latest.current!.loading).toBe(false);
    expect(latest.current!.loadingMore).toBe(false); // 不得卡死
    expect(latest.current!.items.map((t) => t.name)).toEqual(['李一']);

    // 迟到的 张/page2 不得 append；且续拉功能仍然可用。
    await act(async () => { stalePage2.resolve(page([target('u2', '张二')])); });
    await flush(10);
    expect(latest.current!.items.map((t) => t.name)).toEqual(['李一']);

    mockFetch.mockResolvedValueOnce(page([target('l2', '李二')]));
    await act(async () => { await latest.current!.loadMore(); });
    expect(latest.current!.items.map((t) => t.name)).toEqual(['李一', '李二']);
  });
  it('loadMore is a no-op while a page is already in flight or hasMore is false', async () => {
    mockFetch.mockResolvedValue(page([target('u1', '张一')], { hasMore: false }));
    options = { type: 'user', enabled: true, query: '张' };
    await rerender();
    await flush(350);
    expect(latest.current!.hasMore).toBe(false);
    await act(async () => { await latest.current!.loadMore(); });
    expect(mockFetch).toHaveBeenCalledTimes(1); // hasMore=false：不再请求
  });
});

describe('useFeishuTargets — 请求代际（五次复审 P2-8 / §29）', () => {
  it('a slow OLD query can never overwrite the NEW one', async () => {
    // q1=张 请求 A 在途；用户继续输入 q2=张三 请求 B；B 先返回、A 后返回
    // —— 最终列表只能是 B 的结果（旧实现里 A 会覆盖 B）。
    const a = deferred<FeishuForwardTargetPage>();
    const b = deferred<FeishuForwardTargetPage>();
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
    await act(async () => { b.resolve(page([target('u9', '张三本人')])); });
    await flush(10);
    expect(latest.current!.items.map((t) => t.name)).toEqual(['张三本人']);
    expect(latest.current!.loading).toBe(false);

    // A 后返回：整体丢弃，不得覆盖 B。
    await act(async () => { a.resolve(page([target('u1', '张'), target('u2', '张二号')])); });
    await flush(10);
    expect(latest.current!.items.map((t) => t.name)).toEqual(['张三本人']);
  });

  it('clearing the query invalidates an in-flight search immediately', async () => {
    const a = deferred<FeishuForwardTargetPage>();
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
    expect(latest.current!.status).toBe('idle');

    await act(async () => { a.resolve(page([target('u1', '张')])); });
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
    expect(latest.current!.status).toBe('error');
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
      .mockResolvedValueOnce(page([target('u1', '张三')]));
    options = { type: 'user', enabled: true, query: '张三' };
    await rerender();
    await flush(350);
    expect(latest.current!.error).toBeTruthy();

    await act(async () => { await latest.current!.refresh(); });
    await flush(10);
    expect(latest.current!.error).toBeNull();
    expect(latest.current!.items.map((t) => t.name)).toEqual(['张三']);
  });

  it('refresh on chat re-fetches the full list (cache invalidated)', async () => {
    mockFetch
      .mockResolvedValueOnce(page([target('c1', '旧群', 'chat')]))
      .mockResolvedValueOnce(page([target('c2', '新群', 'chat')]));
    options = { type: 'chat', enabled: true, query: '' };
    await rerender();
    await flush(10);
    expect(latest.current!.items.map((t) => t.name)).toEqual(['旧群']);

    await act(async () => { await latest.current!.refresh(); });
    await flush(10);
    expect(mockFetch).toHaveBeenCalledTimes(2);
    expect(latest.current!.items.map((t) => t.name)).toEqual(['新群']);
  });
});

describe('useFeishuTargets — 分页错误与首页错误分离（七次复审 P1-2/P1-3）', () => {
  it('page2 FAILS: page1 rows survive, status stays success, loadMoreError carries the failure', async () => {
    // page1 成功（50 人 + hasMore），page2 网络失败 —— 失败的只是「下一
    // 页」，不是整个数据源：items/status/hasMore 全部保持，错误进独立的
    // loadMoreError（旧实现把 status 拨成 error，Host 会把 50 人全部清空
    // 只剩错误屏）。
    mockFetch
      .mockResolvedValueOnce(page(
        Array.from({ length: 50 }, (_, i) => target(`u${i + 1}`, `员工${i + 1}`)),
        { hasMore: true, cursor: 'p2' },
      ))
      .mockRejectedValueOnce({ response: { status: 502, data: { error: '搜索联系人失败' } } });
    options = { type: 'user', enabled: true, query: '张' };
    await rerender();
    await flush(350);
    expect(latest.current!.items).toHaveLength(50);

    await act(async () => { await latest.current!.loadMore(); });
    await flush(10);
    expect(latest.current!.items).toHaveLength(50); // 已加载页不丢
    expect(latest.current!.status).toBe('success'); // 不是 error
    expect(latest.current!.error).toBeNull(); // 首页错误通道干净
    expect(latest.current!.errorStatus).toBeNull();
    expect(latest.current!.loadMoreError).toBe('搜索联系人失败');
    expect(latest.current!.loadMoreErrorStatus).toBe(502);
    expect(latest.current!.hasMore).toBe(true); // 断点仍在，可重试
    expect(latest.current!.loadingMore).toBe(false);
  });

  it('retrying page2 SUCCEEDS: rows append AND the paging error is truly cleared', async () => {
    // page2 第一次失败 → 再次 loadMore 成功：数据 append 之外，
    // loadMoreError/loadMoreErrorStatus 必须清掉（旧实现不清 error，数据
    // 已恢复而 UI 永远显示失败态）。
    mockFetch
      .mockResolvedValueOnce(page([target('u1', '张一')], { hasMore: true, cursor: 'p2' }))
      .mockRejectedValueOnce({ response: { status: 502, data: { error: '搜索联系人失败' } } })
      .mockResolvedValueOnce(page([target('u2', '张二')], { hasMore: false }));
    options = { type: 'user', enabled: true, query: '张' };
    await rerender();
    await flush(350);
    await act(async () => { await latest.current!.loadMore(); });
    await flush(10);
    expect(latest.current!.loadMoreError).toBeTruthy();

    // 重试失败的那一页（不是 refresh 回第一页）。
    await act(async () => { await latest.current!.loadMore(); });
    await flush(10);
    expect(mockFetch).toHaveBeenLastCalledWith('user', '张', 'p2');
    expect(latest.current!.items.map((t) => t.name)).toEqual(['张一', '张二']);
    expect(latest.current!.loadMoreError).toBeNull();
    expect(latest.current!.loadMoreErrorStatus).toBeNull();
    expect(latest.current!.status).toBe('success');
  });

  it('a loadMore 403 surfaces loadMoreErrorStatus so the host can still offer re-authorization', async () => {
    // 续拉 403 = 授权权限变化（七次复审 §23）：错误拆分后重新授权入口
    // 不能丢 —— loadMoreErrorStatus 必须带出 403。
    mockFetch
      .mockResolvedValueOnce(page([target('u1', '张一')], { hasMore: true, cursor: 'p2' }))
      .mockRejectedValueOnce({ response: { status: 403, data: { detail: '飞书权限不足' } } });
    options = { type: 'user', enabled: true, query: '张' };
    await rerender();
    await flush(350);
    await act(async () => { await latest.current!.loadMore(); });
    await flush(10);
    expect(latest.current!.loadMoreErrorStatus).toBe(403);
    expect(latest.current!.loadMoreError).toBe('飞书权限不足');
    expect(latest.current!.status).toBe('success');
  });

  it('a NEW first page clears a stale paging error from the previous query', async () => {
    // 张/page2 失败后改搜李：新 query 的首页落地时，旧的分页错误属于旧
    // 数据代际，必须清掉 —— 否则李的结果正常展示却仍挂着「更多加载失败」。
    mockFetch
      .mockResolvedValueOnce(page([target('u1', '张一')], { hasMore: true, cursor: 'p2' }))
      .mockRejectedValueOnce({ response: { status: 502, data: { error: '搜索联系人失败' } } })
      .mockResolvedValueOnce(page([target('l1', '李一')]));
    options = { type: 'user', enabled: true, query: '张' };
    await rerender();
    await flush(350);
    await act(async () => { await latest.current!.loadMore(); });
    await flush(10);
    expect(latest.current!.loadMoreError).toBeTruthy();

    options = { ...options, query: '李' };
    await rerender();
    await flush(350);
    expect(latest.current!.loadMoreError).toBeNull();
    expect(latest.current!.items.map((t) => t.name)).toEqual(['李一']);
  });
});

describe('useFeishuTargets — 会话与关闭重置（五次复审 §37–§39）', () => {
  it('disabling the surface invalidates in-flight requests and clears session state', async () => {
    const a = deferred<FeishuForwardTargetPage>();
    mockFetch.mockImplementationOnce(() => a.promise);
    options = { type: 'chat', enabled: true, query: '' };
    await rerender();
    await flush(10);
    expect(latest.current!.loading).toBe(true);

    options = { ...options, enabled: false };
    await rerender();
    await flush(10);
    expect(latest.current!.loading).toBe(false);
    expect(latest.current!.status).toBe('idle');

    // 迟到的响应不得落地。
    await act(async () => { a.resolve(page([target('c1', '群', 'chat')])); });
    await flush(10);
    expect(latest.current!.items).toEqual([]);
  });

  it('chat: re-enabling within the SAME session restores the cache without a new request', async () => {
    // 六次复审 P2-1：切 tab（enabled=false）再切回不算新会话 —— 会话缓存
    // 跨 enabled 切换存续，不重拉全量。
    mockFetch.mockResolvedValue(page([target('c1', '运营群', 'chat')]));
    options = { type: 'chat', enabled: true, query: '', sessionKey: 's1' };
    await rerender();
    await flush(10);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(latest.current!.items.map((t) => t.name)).toEqual(['运营群']);

    options = { ...options, enabled: false };
    await rerender();
    await flush(10);
    expect(latest.current!.items).toEqual([]); // UI 不闪旧数据

    options = { ...options, enabled: true };
    await rerender();
    await flush(10);
    expect(mockFetch).toHaveBeenCalledTimes(1); // 缓存恢复：0 次新请求
    expect(latest.current!.items.map((t) => t.name)).toEqual(['运营群']);
  });

  it('a sessionKey change is a NEW session: immediate reset, no stale-q request, no stale items', async () => {
    // 会话 A：搜索「张伟」成功。
    mockFetch.mockResolvedValue(page([target('u1', '张伟')]));
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
    mockFetch.mockResolvedValue(page([target('c1', '默认群', 'chat')]));
    options = { type: 'chat', enabled: true, query: '', sessionKey: 'b' };
    await rerender();
    await flush(10);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(mockFetch).toHaveBeenLastCalledWith('chat', undefined);
  });
});
