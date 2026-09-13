/**
 * scheduleFormat 纯函数单测：预设转换、字段清理、时区展示。
 */
import { describe, expect, it } from 'vitest';
import {
  WEEKDAY_LABELS,
  cleanTrigger,
  describeTrigger,
  formToPayload,
  scheduleToForm,
  toLocalInput,
  type ScheduleFormValues,
} from '../scheduleFormat';
import type { Schedule } from '@/types/schedule';

const baseForm: ScheduleFormValues = {
  name: '日报',
  application_id: 7,
  prompt: '总结今天',
  schedule_type: 'daily',
  timezone: 'Asia/Shanghai',
  trigger: { time: '09:00', days_of_week: [1, 2], day_of_month: 1 },
  conversation_policy: 'new_each_run',
  overlap_policy: 'queue',
  misfire_policy: 'fire_once',
  deadline_policy: 'execute_anyway',
  execution_window_seconds: 0,
  deliveries: [],
};

describe('formToPayload', () => {
  it('keeps only trigger fields relevant to the schedule type', () => {
    const p = formToPayload({ ...baseForm, schedule_type: 'weekly', trigger: { time: '08:30', days_of_week: [3, 1, 5], day_of_month: 31 } });
    expect(p.trigger).toEqual({ time: '08:30', days_of_week: [1, 3, 5] });
  });

  it('monthly keeps day_of_month and drops days_of_week', () => {
    const p = formToPayload({ ...baseForm, schedule_type: 'monthly' });
    expect(p.trigger).toEqual({ time: '09:00', day_of_month: 1 });
  });

  it('once uses run_at and drops trigger', () => {
    const p = formToPayload({ ...baseForm, schedule_type: 'once', run_at_local: '2026-09-14T09:00' });
    expect(p.trigger).toBeUndefined();
    expect(p.run_at).toBeTruthy();
  });

  it('empty deliveries become undefined (no delivery)', () => {
    expect(formToPayload(baseForm).deliveries).toBeUndefined();
  });

  it('deliveries map to summary content mode', () => {
    const p = formToPayload({
      ...baseForm,
      deliveries: [{ target_type: 'chat', target_id: 'oc_1', target_name: '群' }],
    });
    expect(p.deliveries).toEqual([
      { target_type: 'chat', target_id: 'oc_1', target_name: '群', content_mode: 'summary' },
    ]);
  });
});

describe('describeTrigger', () => {
  it('formats daily/weekly/monthly in Chinese', () => {
    expect(describeTrigger('daily', { time: '09:00' })).toBe('每天 09:00');
    expect(describeTrigger('weekly', { time: '09:00', days_of_week: [1, 5] })).toContain('周一');
    expect(describeTrigger('monthly', { time: '09:00', day_of_month: 31 })).toContain('31 号');
  });

  it('WEEKDAY_LABELS start on Sunday (0)', () => {
    expect(WEEKDAY_LABELS[0]).toBe('周日');
    expect(WEEKDAY_LABELS[1]).toBe('周一');
  });
});

describe('scheduleToForm roundtrip', () => {
  it('hydrates the editor form from a Schedule', () => {
    const schedule = {
      id: 1,
      name: '周报',
      description: '',
      application_id: 3,
      prompt: '写周报',
      schedule_type: 'weekly' as const,
      cron_expression: '0 9 * * 1',
      timezone: 'UTC',
      run_at: null,
      trigger: { time: '09:00', days_of_week: [1] },
      enabled: true,
      conversation_policy: 'new_each_run' as const,
      overlap_policy: 'queue' as const,
      misfire_policy: 'fire_once' as const,
      deadline_policy: 'execute_anyway' as const,
      execution_window_seconds: 0,
      next_run_at: null,
      last_run_at: null,
      created_at: '2026-09-13T00:00:00Z',
      updated_at: '2026-09-13T00:00:00Z',
      deliveries: [],
    } as Schedule;
    const form = scheduleToForm(schedule);
    expect(form.schedule_type).toBe('weekly');
    expect(form.trigger.days_of_week).toEqual([1]);
    expect(form.timezone).toBe('UTC');
  });
});

describe('toLocalInput', () => {
  it('renders a datetime-local value', () => {
    const out = toLocalInput('2026-09-13T01:02:00Z');
    expect(out).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/);
  });

  it('returns empty for invalid input', () => {
    expect(toLocalInput('not-a-date')).toBe('');
  });
});

describe('cleanTrigger (exported helper)', () => {
  it('strips irrelevant fields for daily', () => {
    expect(cleanTrigger('daily', { time: '09:00', days_of_week: [1], day_of_month: 2 }))
      .toEqual({ time: '09:00' });
  });
});
