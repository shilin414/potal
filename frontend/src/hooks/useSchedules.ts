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
  mutatingId: number | null;
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
  const [mutatingId, setMutatingId] = useState<number | null>(null);
  const seqRef = useRef(0);

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
      if (seq === seqRef.current) {
        setLoadingMore(false);
      }
    }
  }, [status, debouncedSearch, data, loading, loadingMore, hasMore]);

  const patchLocal = useCallback((id: number, patch: Partial<Schedule>) => {
    setData((prev) => prev.map((s) => (s.id === id ? { ...s, ...patch } : s)));
  }, []);

  const toggleEnabled = useCallback(async (id: number, enabled: boolean) => {
    setMutatingId(id);
    const rollback = () => patchLocal(id, { enabled: !enabled });
    patchLocal(id, { enabled }); // 乐观更新，失败回滚
    try {
      const updated = enabled ? await enableSchedule(id) : await disableSchedule(id);
      patchLocal(id, updated);
    } catch (e) {
      rollback();
      message.error(e instanceof Error ? e.message : (enabled ? '启用失败' : '停用失败'));
    } finally {
      setMutatingId(null);
    }
  }, [patchLocal]);

  const runNow = useCallback(async (id: number) => {
    setMutatingId(id);
    try {
      await runScheduleNow(id);
      message.success('已加入执行队列');
      await load(status, debouncedSearch, 'refresh');
      return true;
    } catch (e) {
      message.error(e instanceof Error ? e.message : '立即运行失败');
      return false;
    } finally {
      setMutatingId(null);
    }
  }, [load, status, debouncedSearch]);

  const remove = useCallback(async (id: number) => {
    setMutatingId(id);
    try {
      await deleteSchedule(id);
      message.success('定时任务已删除');
      setData((prev) => prev.filter((s) => s.id !== id));
      return true;
    } catch (e) {
      message.error(e instanceof Error ? e.message : '删除失败');
      return false;
    } finally {
      setMutatingId(null);
    }
  }, []);

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
    mutatingId,
    setStatus,
    setSearch,
    reload,
    toggleEnabled,
    runNow,
    remove,
  };
}
