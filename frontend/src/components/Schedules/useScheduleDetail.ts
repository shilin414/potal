/**
 * useScheduleDetail — 配置 + 执行记录的加载逻辑，桌面 Drawer 与移动
 * 全屏详情共用（开发执行报告 §30）。
 *
 * 任务详情与执行记录是两个失败域（三次复审 §36–§37）：Occurrences 是辅助
 * 历史数据，它的接口故障不能让任务本身的名称/计划/Prompt 不可查看。
 * 两者独立加载、独立重试。
 *
 * 执行记录分页（三次复审 §33–§35）：后端 occurrences 也是 keyset（before_id
 * + DESC id）裸数组，同样用 51 probe / 50 display 推导 hasMore —— 第 51 条
 * 以前的执行历史不再永远不可见。
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { fetchSchedule, fetchScheduleOccurrences } from '@/services/scheduleApi';
import type { Schedule, ScheduleOccurrence } from '@/types/schedule';

/** 执行记录每页展示条数；请求 +1 作 hasMore 探针。 */
const OCCURRENCES_PAGE_SIZE = 50;

/** 51 probe / 50 display：返回 51 条 → 展示前 50 且 hasMore。 */
const splitOccurrences = (items: ScheduleOccurrence[]) => {
  const more = items.length > OCCURRENCES_PAGE_SIZE;
  return {
    page: more ? items.slice(0, OCCURRENCES_PAGE_SIZE) : items,
    more,
  };
};

export interface UseScheduleDetailResult {
  schedule: Schedule | null;
  loading: boolean;
  error: string | null;

  occurrences: ScheduleOccurrence[];
  occurrencesLoading: boolean;
  occurrenceError: string | null;

  hasMoreOccurrences: boolean;
  loadingMoreOccurrences: boolean;
  loadMoreOccurrences(): Promise<void>;

  /** 执行记录独立重试（不影响已加载的任务配置）。 */
  retryOccurrences(): Promise<void>;
  /** 任务配置独立重试（四次复审 P2-3）：配置加载失败不再只能关抽屉重开。 */
  retrySchedule(): Promise<void>;
}

export function useScheduleDetail(
  open: boolean,
  scheduleId: number | null,
): UseScheduleDetailResult {
  const [schedule, setSchedule] = useState<Schedule | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [occurrences, setOccurrences] = useState<ScheduleOccurrence[]>([]);
  const [occurrencesLoading, setOccurrencesLoading] = useState(false);
  const [occurrenceError, setOccurrenceError] = useState<string | null>(null);
  const [hasMoreOccurrences, setHasMoreOccurrences] = useState(false);
  const [loadingMoreOccurrences, setLoadingMoreOccurrences] = useState(false);

  const scheduleSeqRef = useRef(0);
  const occSeqRef = useRef(0);
  // loadMore generation（四次复审 P1-2）：目标变化/新首页开始的瞬间立即作废
  // 在途翻页 —— spinner 当场归还（不等旧 HTTP 自己结束），且旧 finally 关不
  // 掉更新 loadMore 的 spinner。只有最新的 loadMore 拥有 spinner 状态。
  const occLoadMoreSeqRef = useRef(0);

  // 目标变化与「关闭」都清空两域旧数据：关闭时的清空发生在抽屉不可见期
  // 间，重开新目标不会在首帧闪现上一个任务的配置/执行记录（复审 P1）。
  useEffect(() => {
    setSchedule(null);
    setOccurrences([]);
    setOccurrenceError(null);
    setError(null);
    setHasMoreOccurrences(false);
  }, [open, scheduleId]);

  // 任务配置：主数据，独立失败域（§37）。提取为可重试的加载函数（四次复审
  // P2-3）——配置失败不再只能「关抽屉重开」，原地给重试按钮。
  const loadSchedule = useCallback(async () => {
    if (!open || !scheduleId) return;
    const seq = ++scheduleSeqRef.current;
    setLoading(true);
    setError(null);
    try {
      const s = await fetchSchedule(scheduleId);
      if (seq === scheduleSeqRef.current) setSchedule(s);
    } catch (e) {
      if (seq === scheduleSeqRef.current) {
        setError(e instanceof Error ? e.message : '加载失败');
      }
    } finally {
      if (seq === scheduleSeqRef.current) setLoading(false);
    }
  }, [open, scheduleId]);

  useEffect(() => {
    void loadSchedule();
    return () => {
      scheduleSeqRef.current += 1;
    };
  }, [loadSchedule]);

  // 执行记录：辅助历史数据，独立加载 + 独立重试（§37），分页（§35）。
  const loadOccurrences = useCallback(async () => {
    if (!open || !scheduleId) return;
    const seq = ++occSeqRef.current;
    // 新结果集 → 在途翻页立即作废（四次复审 P1-2）。
    occLoadMoreSeqRef.current += 1;
    setLoadingMoreOccurrences(false);
    setOccurrencesLoading(true);
    setOccurrenceError(null);
    try {
      const occs = await fetchScheduleOccurrences(
        scheduleId, undefined, OCCURRENCES_PAGE_SIZE + 1);
      if (seq !== occSeqRef.current) return;
      const { page, more } = splitOccurrences(occs);
      setOccurrences(page);
      setHasMoreOccurrences(more);
    } catch (e) {
      if (seq !== occSeqRef.current) return;
      setOccurrenceError(e instanceof Error ? e.message : '加载执行记录失败');
    } finally {
      if (seq === occSeqRef.current) setOccurrencesLoading(false);
    }
  }, [open, scheduleId]);

  useEffect(() => {
    void loadOccurrences();
    return () => {
      // 关闭/目标切换废弃在途的执行记录请求：旧响应不得回填新目标。
      occSeqRef.current += 1;
    };
  }, [loadOccurrences]);

  const loadMoreOccurrences = useCallback(async () => {
    if (!open || !scheduleId) return;
    if (loadingMoreOccurrences || occurrencesLoading || !hasMoreOccurrences) return;
    const beforeId = occurrences[occurrences.length - 1]?.id;
    if (!beforeId) return;
    // 翻页与首页同代（不 bump seq）：目标变化会 bump，在途翻页自动作废。
    const seq = occSeqRef.current;
    const myLoadMore = ++occLoadMoreSeqRef.current;
    setLoadingMoreOccurrences(true);
    try {
      const occs = await fetchScheduleOccurrences(
        scheduleId, beforeId, OCCURRENCES_PAGE_SIZE + 1);
      if (seq !== occSeqRef.current) return;
      const { page, more } = splitOccurrences(occs);
      setOccurrences((prev) => [...prev, ...page]);
      setHasMoreOccurrences(more);
      // 重试成功清错误（与列表 hook 同一规则）。
      setOccurrenceError(null);
    } catch (e) {
      if (seq !== occSeqRef.current) return;
      // 已加载的执行记录保留；下一次点击 loadMore 即重试。
      setOccurrenceError(e instanceof Error ? e.message : '加载更多执行记录失败');
    } finally {
      // 只有最新的 loadMore 拥有 spinner（四次复审 P1-2）：目标切换时已
      // bump 代际并复位 spinner，旧请求迟到的 finally 不得再碰它，也永远
      // 不能关掉更新 loadMore 的 spinner。
      if (occLoadMoreSeqRef.current === myLoadMore) {
        setLoadingMoreOccurrences(false);
      }
    }
  }, [
    open, scheduleId, occurrences, loadingMoreOccurrences,
    occurrencesLoading, hasMoreOccurrences,
  ]);

  return {
    schedule,
    loading,
    error,
    occurrences,
    occurrencesLoading,
    occurrenceError,
    hasMoreOccurrences,
    loadingMoreOccurrences,
    loadMoreOccurrences,
    retryOccurrences: loadOccurrences,
    retrySchedule: loadSchedule,
  };
}
