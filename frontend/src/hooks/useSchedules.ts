/**
 * useSchedules — 定时任务列表 hook。
 * 只保存 UI 无关的服务端事实到内存（不持久化），行级 mutation 单独锁定，
 * 过期请求不会覆盖新数据（sequence guard）。
 *
 * 分页（三次复审 P1 §17–§22）：后端 /v2/schedules 是 keyset（before_id +
 * DESC id），接口回裸数组没有 next_cursor，所以用 51 probe / 50 display 推
 * 导 hasMore —— 请求 limit = 50+1，返回 51 条 → 只展示前 50 条且 hasMore；
 * ≤50 条 → 全部展示且没有更多。loadMore 用已展示最后一行的 id 作 before_id。
 *
 * 搜索（三次复审 P1 §23–§28）：q 直发后端（300ms 防抖），覆盖全部任务
 * 而不只是已加载页 —— 搜索词变化重置回第一页。浏览器端不再本地过滤。
 * Desktop 表格与 Mobile 卡片共用本 hook，任何一端不得自行请求 API。
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { message } from 'antd';
import type { Schedule, ScheduleStatusFilter } from '@/types/schedule';
import {
  deleteSchedule,
  disableSchedule,
  enableSchedule,
  fetchSchedules,
  runScheduleNow,
} from '@/services/scheduleApi';

/** 每页展示条数；实际请求 +1 作 hasMore 探针（三次复审 §21）。 */
const SCHEDULES_PAGE_SIZE = 50;

/** 搜索防抖（三次复审 §28）。 */
const SEARCH_DEBOUNCE_MS = 300;

/** 51 probe / 50 display：返回 51 条 → 展示前 50 且 hasMore。 */
const splitPage = (items: Schedule[]) => {
  const more = items.length > SCHEDULES_PAGE_SIZE;
  return { page: more ? items.slice(0, SCHEDULES_PAGE_SIZE) : items, more };
};

/**
 * 失败发生在哪一阶段（三次复审 §30–§32）：
 *   initial  — 挂载/筛选变化后的第一页，且还没有任何数据 → fatal；
 *   refresh  — 显式刷新/搜索失败，但已有数据 → partial（旧数据保留）；
 *   loadMore — 翻页失败 → 底部 CTA 变重试，已加载行保留。
 */
export type SchedulesErrorPhase = 'initial' | 'refresh' | 'loadMore';

export interface UseSchedulesResult {
  data: Schedule[];

  loading: boolean;
  loadingMore: boolean;

  error: string | null;
  errorPhase: SchedulesErrorPhase | null;
  hasMore: boolean;

  loadMore(): Promise<void>;

  status: ScheduleStatusFilter;
  search: string;
  /** 行级 mutation 并发跟踪（四次复审 P2-1）：任务 A 暂停与任务 B 立即运行可以同时在途。 */
  isMutating: (id: number) => boolean;
  setStatus: (s: ScheduleStatusFilter) => void;
  setSearch: (s: string) => void;
  reload: () => Promise<void>;
  toggleEnabled: (id: number, enabled: boolean) => Promise<void>;
  runNow: (id: number) => Promise<boolean>;
  remove: (id: number) => Promise<boolean>;
}

export function useSchedules(): UseSchedulesResult {
  const [data, setData] = useState<Schedule[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [errorPhase, setErrorPhase] = useState<SchedulesErrorPhase | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [status, setStatus] = useState<ScheduleStatusFilter>('all');
  const [search, setSearch] = useState('');
  // 行级 mutation 用 Set 而不是单值（四次复审 P2-1）：mutatingId = A 会被
  // B 的开始覆盖，A 先结束时置 null 让仍在途的 B 看起来「不在执行」——
  // 用户可以对同一行重复点击。每行独立进出，并发互不干扰。
  const [mutatingIds, setMutatingIds] = useState<Set<number>>(new Set());
  const beginMutation = useCallback((id: number) => {
    setMutatingIds((prev) => {
      const next = new Set(prev);
      next.add(id);
      return next;
    });
  }, []);
  const endMutation = useCallback((id: number) => {
    setMutatingIds((prev) => {
      const next = new Set(prev);
      next.delete(id);
      return next;
    });
  }, []);
  const isMutating = useCallback(
    (id: number) => mutatingIds.has(id), [mutatingIds]);
  const seqRef = useRef(0);
  // loadMore generation（四次复审 P1-2）：结果集代际（seqRef）与翻页操作代际
  // 是两个概念。新结果集（筛选/搜索/刷新）开始的瞬间立即作废旧结果集的
  // 在途 loadMore —— spinner 当场归还，而不是等旧 HTTP 自己结束（卡住 60s
  // 的旧请求不能挡住 paused 列表继续翻页）；同时旧 loadMore 迟到的 finally
  // 也永远关不掉更新的 loadMore 的 spinner。
  const loadMoreSeqRef = useRef(0);

  // 防抖只平滑「打字」：status 切换与首次挂载立即请求。
  const [debouncedSearch, setDebouncedSearch] = useState('');
  useEffect(() => {
    if (search === debouncedSearch) return undefined;
    const timer = setTimeout(() => setDebouncedSearch(search), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [search, debouncedSearch]);

  const load = useCallback(async (
    filter: ScheduleStatusFilter,
    q: string,
    phase: SchedulesErrorPhase,
  ) => {
    const seq = ++seqRef.current;
    // 新结果集 → 旧结果集的在途 loadMore 立即作废（四次复审 P1-2）。
    loadMoreSeqRef.current += 1;
    setLoadingMore(false);
    setLoading(true);
    setError(null);
    setErrorPhase(null);
    try {
      const items = await fetchSchedules(
        filter, undefined, SCHEDULES_PAGE_SIZE + 1, q);
      if (seq === seqRef.current) {
        const { page, more } = splitPage(items);
        setData(page);
        setHasMore(more);
      }
    } catch (e) {
      if (seq === seqRef.current) {
        // 失败保留旧数据（三次复审 §30）：加载过 50 条真实任务的列表
        // 不能因为一次刷新失败整体消失。
        setError(e instanceof Error ? e.message : '加载定时任务失败');
        setErrorPhase(phase);
        // 首页失败清掉 cursor（重试必须回到第一页，而不是带着旧
        // cursor 跳过新首页）——与 useDirectoryUsers 同一规则。
        setHasMore(false);
      }
    } finally {
      if (seq === seqRef.current) {
        setLoading(false);
      }
    }
  }, []);

  // status / debouncedSearch 任一变化 = 新结果集：清 cursor，回第一页。
  useEffect(() => {
    void load(status, debouncedSearch, 'initial');
  }, [load, status, debouncedSearch]);

  const reload = useCallback(async () => {
    await load(status, debouncedSearch, 'refresh');
  }, [load, status, debouncedSearch]);

  const loadMore = useCallback(async () => {
    if (loading || loadingMore || !hasMore) return;
    const beforeId = data[data.length - 1]?.id;
    if (!beforeId) return;
    // 翻页与首页同代（不 bump seq）：筛选变化会 bump，在途翻页自动作废。
    const seq = seqRef.current;
    const myLoadMore = ++loadMoreSeqRef.current;
    setLoadingMore(true);
    try {
      const items = await fetchSchedules(
        status, beforeId, SCHEDULES_PAGE_SIZE + 1, debouncedSearch);
      if (seq === seqRef.current) {
        const { page, more } = splitPage(items);
        setData((prev) => [...prev, ...page]);
        setHasMore(more);
        // 重试成功清掉「加载更多失败」（与 useDirectoryUsers 同一规则）。
        setError(null);
        setErrorPhase(null);
      }
    } catch (e) {
      if (seq === seqRef.current) {
        // 已加载行保留；下一次点击 loadMore 即重试。
        setError(e instanceof Error ? e.message : '加载更多失败');
        setErrorPhase('loadMore');
      }
    } finally {
      // 只有最新的 loadMore 拥有 spinner（四次复审 P1-2）：新结果集开始时
      // 已 bump loadMoreSeq 并复位 spinner —— 旧请求迟到的 finally 既不该
      // 再碰它，也永远不能关掉更新 loadMore 的 spinner。
      if (loadMoreSeqRef.current === myLoadMore) {
        setLoadingMore(false);
      }
    }
  }, [status, debouncedSearch, data, loading, loadingMore, hasMore]);

  const patchLocal = useCallback((id: number, patch: Partial<Schedule>) => {
    setData((prev) => prev.map((s) => (s.id === id ? { ...s, ...patch } : s)));
  }, []);

  const toggleEnabled = useCallback(async (id: number, enabled: boolean) => {
    beginMutation(id);
    const rollback = () => patchLocal(id, { enabled: !enabled });
    patchLocal(id, { enabled }); // 乐观更新，失败回滚
    try {
      const updated = enabled ? await enableSchedule(id) : await disableSchedule(id);
      // 与服务端筛选语义对齐（四次复审 P1-4）：running = enabled、
      // paused = disabled、failed/all 与 enabled 无关。停用一个任务后它就
      // 不再属于「运行中」结果集 —— 留在列表里只是行内状态翻转，与后端
      // 重新查询的语义冲突。在当前结果集内做成员归并（而不是 reload，
      // reload 会把用户已 loadMore 的 150 条打回第一页 50 条）。
      const belongs =
        status === 'all'
        || status === 'failed'
        || (status === 'running' && updated.enabled)
        || (status === 'paused' && !updated.enabled);
      if (belongs) {
        patchLocal(id, updated);
      } else {
        setData((prev) => prev.filter((s) => s.id !== id));
      }
    } catch (e) {
      rollback();
      message.error(e instanceof Error ? e.message : (enabled ? '启用失败' : '停用失败'));
    } finally {
      endMutation(id);
    }
  }, [patchLocal, status, beginMutation, endMutation]);

  const runNow = useCallback(async (id: number) => {
    beginMutation(id);
    try {
      await runScheduleNow(id);
      message.success('已加入执行队列');
      await load(status, debouncedSearch, 'refresh');
      return true;
    } catch (e) {
      message.error(e instanceof Error ? e.message : '立即运行失败');
      return false;
    } finally {
      endMutation(id);
    }
  }, [load, status, debouncedSearch, beginMutation, endMutation]);

  const remove = useCallback(async (id: number) => {
    beginMutation(id);
    try {
      await deleteSchedule(id);
      message.success('定时任务已删除');
      setData((prev) => prev.filter((s) => s.id !== id));
      return true;
    } catch (e) {
      message.error(e instanceof Error ? e.message : '删除失败');
      return false;
    } finally {
      endMutation(id);
    }
  }, [beginMutation, endMutation]);

  return {
    data,
    loading,
    loadingMore,
    error,
    errorPhase,
    hasMore,
    loadMore,
    status,
    search,
    isMutating,
    setStatus,
    setSearch,
    reload,
    toggleEnabled,
    runNow,
    remove,
  };
}
