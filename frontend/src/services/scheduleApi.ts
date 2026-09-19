/**
 * 定时任务 API client（typed — 禁止 Record<string, any>）。
 * URL 以 /v2 开头（axios 实例 baseURL 已含 /api）。
 */
import { api } from '@/services/api';
import type {
  Schedule,
  ScheduleOccurrence,
  ScheduleStatusFilter,
  ScheduleUpsertPayload,
} from '@/types/schedule';

export async function fetchSchedules(
  status: ScheduleStatusFilter = 'all',
  beforeId?: number,
  limit = 50,
  q?: string,
): Promise<Schedule[]> {
  const params: Record<string, number | string> = { limit, status };
  if (beforeId) params.before_id = beforeId;
  const needle = q?.trim();
  if (needle) params.q = needle;
  return api.get<Schedule[]>('/v2/schedules', params);
}

export async function fetchSchedule(id: number): Promise<Schedule> {
  return api.get<Schedule>(`/v2/schedules/${id}`);
}

export async function createSchedule(payload: ScheduleUpsertPayload): Promise<Schedule> {
  return api.post<Schedule>('/v2/schedules', payload);
}

export async function updateSchedule(
  id: number,
  payload: Partial<ScheduleUpsertPayload>,
): Promise<Schedule> {
  return api.patch<Schedule>(`/v2/schedules/${id}`, payload);
}

export async function deleteSchedule(id: number): Promise<void> {
  return api.delete<void>(`/v2/schedules/${id}`);
}

export async function enableSchedule(id: number): Promise<Schedule> {
  return api.post<Schedule>(`/v2/schedules/${id}/enable`);
}

export async function disableSchedule(id: number): Promise<Schedule> {
  return api.post<Schedule>(`/v2/schedules/${id}/disable`);
}

export async function runScheduleNow(id: number): Promise<ScheduleOccurrence> {
  return api.post<ScheduleOccurrence>(`/v2/schedules/${id}/run-now`);
}

export async function fetchScheduleOccurrences(
  id: number,
  beforeId?: number,
  limit = 50,
): Promise<ScheduleOccurrence[]> {
  const params: Record<string, number> = { limit };
  if (beforeId) params.before_id = beforeId;
  return api.get<ScheduleOccurrence[]>(
    `/v2/schedules/${id}/occurrences`, params);
}

/** 权威的下次执行预览（后端计算，前端不预测时间）。 */
export async function previewScheduleRuns(
  payload: Partial<ScheduleUpsertPayload>,
): Promise<string[]> {
  const res = await api.post<{ next_runs: string[] }>('/v2/schedules/preview', payload);
  return res.next_runs ?? [];
}
