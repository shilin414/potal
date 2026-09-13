/**
 * 定时任务展示格式化（纯函数，Vitest 直接覆盖）。
 * 与后端契约对齐：weekly 的 days_of_week 0=周日；monthly 短月顺延。
 */
import type { Schedule, ScheduleType, ScheduleTrigger, ScheduleUpsertPayload } from '@/types/schedule';

export const WEEKDAY_LABELS = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];

/** trigger → 人类可读计划摘要（无时区，时区单独展示）。 */
export function describeTrigger(scheduleType: ScheduleType, trigger?: ScheduleTrigger, runAt?: string | null): string {
  switch (scheduleType) {
    case 'once':
      return runAt ? formatDateTime(runAt) : '单次执行（未指定时间）';
    case 'daily':
      return `每天 ${trigger?.time ?? '--:--'}`;
    case 'weekly': {
      const days = (trigger?.days_of_week ?? []).slice().sort((a, b) => a - b);
      const label = days.map((d) => WEEKDAY_LABELS[d] ?? '?').join('、');
      return `每周 ${label || '—'} ${trigger?.time ?? '--:--'}`;
    }
    case 'monthly':
      return `每月 ${trigger?.day_of_month ?? '?'} 号 ${trigger?.time ?? '--:--'}（短月顺延到月末）`;
    default:
      return '未配置';
  }
}

/** 面向列表的完整摘要（带时区）。 */
export function describeSchedulePlan(schedule: Pick<Schedule, 'schedule_type' | 'trigger' | 'run_at' | 'timezone'>): string {
  return `${describeTrigger(schedule.schedule_type, schedule.trigger, schedule.run_at)} · ${schedule.timezone}`;
}

export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export interface ScheduleFormValues {
  name: string;
  description?: string;
  application_id: number;
  prompt: string;
  schedule_type: ScheduleType;
  timezone: string;
  /** once 模式的本地时间输入值（datetime-local）。 */
  run_at_local?: string;
  trigger: ScheduleTrigger;
  conversation_policy: 'new_each_run' | 'reuse';
  overlap_policy: 'skip' | 'queue';
  misfire_policy: 'fire_once' | 'skip';
  deadline_policy: 'skip' | 'execute_anyway';
  execution_window_seconds: number;
  /** 飞书投递目标（空 = 不投递）。 */
  deliveries: ScheduleFormDelivery[];
}

export interface ScheduleFormDelivery {
  target_type: 'user' | 'chat';
  target_id: string;
  target_name: string;
}

/** 表单值 → 创建/更新 payload（清理无关字段）。 */
export function formToPayload(v: ScheduleFormValues): ScheduleUpsertPayload {
  const payload: ScheduleUpsertPayload = {
    name: v.name.trim(),
    description: v.description?.trim() || undefined,
    application_id: v.application_id,
    prompt: v.prompt,
    schedule_type: v.schedule_type,
    timezone: v.timezone,
    conversation_policy: v.conversation_policy,
    overlap_policy: v.overlap_policy,
    misfire_policy: v.misfire_policy,
    deadline_policy: v.deadline_policy,
    execution_window_seconds: v.execution_window_seconds,
    deliveries: v.deliveries.length > 0
      ? v.deliveries.map((d) => ({
          target_type: d.target_type,
          target_id: d.target_id,
          target_name: d.target_name,
          content_mode: 'summary' as const,
        }))
      : undefined,
  };
  if (v.schedule_type === 'once') {
    payload.run_at = toISO(v.run_at_local);
    payload.trigger = undefined;
  } else {
    payload.trigger = cleanTrigger(v.schedule_type, v.trigger);
  }
  return payload;
}

/** datetime-local 字符串或 Dayjs（ duck-typed）→ ISO 字符串。 */
function toISO(v: unknown): string | undefined {
  if (!v) return undefined;
  if (typeof v === 'string') {
    const d = new Date(v);
    return Number.isNaN(d.getTime()) ? undefined : d.toISOString();
  }
  const anyV = v as { toISOString?: () => string };
  if (typeof anyV.toISOString === 'function') return anyV.toISOString();
  return undefined;
}

/** 清理与 schedule_type 无关的字段，避免提交噪声。 */
export function cleanTrigger(type: ScheduleType, trigger: ScheduleTrigger): ScheduleTrigger {
  const out: ScheduleTrigger = {};
  if (type !== 'once') out.time = trigger.time;
  if (type === 'weekly') out.days_of_week = (trigger.days_of_week ?? []).slice().sort((a, b) => a - b);
  if (type === 'monthly') out.day_of_month = trigger.day_of_month;
  return out;
}

/** Schedule → 表单初值（编辑回填）。 */
export function scheduleToForm(s: Schedule): ScheduleFormValues {
  return {
    name: s.name,
    description: s.description || undefined,
    application_id: s.application_id,
    prompt: s.prompt,
    schedule_type: s.schedule_type,
    timezone: s.timezone,
    run_at_local: s.run_at ? toLocalInput(s.run_at) : undefined,
    trigger: {
      time: s.trigger?.time ?? '09:00',
      days_of_week: s.trigger?.days_of_week ?? [1],
      day_of_month: s.trigger?.day_of_month ?? 1,
    },
    conversation_policy: s.conversation_policy,
    overlap_policy: s.overlap_policy,
    misfire_policy: s.misfire_policy,
    deadline_policy: s.deadline_policy,
    execution_window_seconds: s.execution_window_seconds,
    deliveries: (s.deliveries ?? []).map((d) => ({
      target_type: d.target_type,
      target_id: d.target_id,
      target_name: d.target_name,
    })),
  };
}

/** ISO → datetime-local 输入值（本地时区）。 */
export function toLocalInput(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
