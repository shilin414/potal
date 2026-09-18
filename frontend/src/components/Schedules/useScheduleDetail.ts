/**
 * useScheduleDetail — 配置 + 执行记录的加载逻辑，桌面 Drawer 与移动
 * 全屏详情共用（开发执行报告 §30）。
 */
import { useEffect, useState } from 'react';
import { fetchSchedule, fetchScheduleOccurrences } from '@/services/scheduleApi';
import type { Schedule, ScheduleOccurrence } from '@/types/schedule';

export function useScheduleDetail(open: boolean, scheduleId: number | null) {
  const [schedule, setSchedule] = useState<Schedule | null>(null);
  const [occurrences, setOccurrences] = useState<ScheduleOccurrence[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open || !scheduleId) return;
    let stale = false;
    setLoading(true);
    setError(null);
    Promise.all([fetchSchedule(scheduleId), fetchScheduleOccurrences(scheduleId)])
      .then(([s, occs]) => {
        if (stale) return;
        setSchedule(s);
        setOccurrences(occs);
      })
      .catch((e) => {
        if (!stale) setError(e instanceof Error ? e.message : '加载失败');
      })
      .finally(() => {
        if (!stale) setLoading(false);
      });
    return () => {
      stale = true;
    };
  }, [open, scheduleId]);

  return { schedule, occurrences, loading, error };
}
