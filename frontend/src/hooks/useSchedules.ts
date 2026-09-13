/**
 * useSchedules — 定时任务列表 hook。
 * 只保存 UI 无关的服务端事实到内存（不持久化），行级 mutation 单独锁定，
 * 过期请求不会覆盖新数据（sequence guard）。
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

export interface UseSchedulesResult {
  data: Schedule[];
  loading: boolean;
  error: string | null;
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
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<ScheduleStatusFilter>('all');
  const [search, setSearch] = useState('');
  const [mutatingId, setMutatingId] = useState<number | null>(null);
  const seqRef = useRef(0);

  const load = useCallback(async (filter: ScheduleStatusFilter) => {
    const seq = ++seqRef.current;
    setLoading(true);
    setError(null);
    try {
      const items = await fetchSchedules(filter);
      if (seq === seqRef.current) {
        setData(items);
      }
    } catch (e) {
      if (seq === seqRef.current) {
        setError(e instanceof Error ? e.message : '加载定时任务失败');
      }
    } finally {
      if (seq === seqRef.current) {
        setLoading(false);
      }
    }
  }, []);

  useEffect(() => {
    void load(status);
  }, [load, status]);

  const reload = useCallback(async () => {
    await load(status);
  }, [load, status]);

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
      await load(status);
      return true;
    } catch (e) {
      message.error(e instanceof Error ? e.message : '立即运行失败');
      return false;
    } finally {
      setMutatingId(null);
    }
  }, [load, status]);

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

  const filtered = search.trim()
    ? data.filter((s) =>
        s.name.toLowerCase().includes(search.trim().toLowerCase()))
    : data;

  return {
    data: filtered,
    loading,
    error,
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
