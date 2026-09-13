/**
 * 定时任务（Schedule）领域类型。
 *
 * 领域边界（架构文档）：Schedule 只回答「什么时候自动创建 Run」，
 * 每次真实执行是一条 Run，投递（飞书消息）由 DeliveryExecution 承载，
 * 三者状态互相独立。
 */

export type ScheduleType = 'once' | 'daily' | 'weekly' | 'monthly';

export type ScheduleStatusFilter = 'all' | 'running' | 'paused' | 'failed';

export type OccurrenceStatus =
  | 'pending'
  | 'queued'
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'skipped';

export type ConversationPolicy = 'new_each_run' | 'reuse';
export type OverlapPolicy = 'skip' | 'queue';
export type MisfirePolicy = 'fire_once' | 'skip';
export type DeadlinePolicy = 'skip' | 'execute_anyway';

export interface ScheduleTrigger {
  /** "HH:MM"（任务时区） */
  time?: string;
  /** 0=周日 … 6=周六（weekly） */
  days_of_week?: number[];
  /** 1..31（monthly，短月顺延到当月最后一天） */
  day_of_month?: number;
}

export interface ScheduleDeliveryInput {
  target_type: 'user' | 'chat';
  target_id: string;
  target_name?: string;
  content_mode?: 'summary';
}

export interface ScheduleUpsertPayload {
  name: string;
  description?: string;
  application_id: number;
  prompt: string;
  schedule_type: ScheduleType;
  timezone?: string;
  run_at?: string;
  trigger?: ScheduleTrigger;
  conversation_policy?: ConversationPolicy;
  overlap_policy?: OverlapPolicy;
  misfire_policy?: MisfirePolicy;
  deadline_policy?: DeadlinePolicy;
  execution_window_seconds?: number;
  deliveries?: ScheduleDeliveryInput[];
}

export interface ScheduleDelivery {
  id: number;
  target_type: 'user' | 'chat';
  target_id: string;
  target_name: string;
  content_mode: string;
  enabled: boolean;
}

export interface ScheduleOccurrence {
  id: number;
  schedule_id: number;
  scheduled_at: string;
  enqueued_at: string | null;
  admitted_at: string | null;
  run_id?: string;
  status: OccurrenceStatus;
  triggered_at: string | null;
  finished_at: string | null;
  created_at: string;
}

export interface Schedule {
  id: number;
  name: string;
  description: string;
  application_id: number;
  prompt: string;
  schedule_type: ScheduleType;
  /** 信息性字段：由 trigger 推导，用户不输入。 */
  cron_expression: string;
  timezone: string;
  run_at: string | null;
  trigger: ScheduleTrigger;
  enabled: boolean;
  conversation_policy: ConversationPolicy;
  overlap_policy: OverlapPolicy;
  misfire_policy: MisfirePolicy;
  deadline_policy: DeadlinePolicy;
  execution_window_seconds: number;
  next_run_at: string | null;
  last_run_at: string | null;
  created_at: string;
  updated_at: string;
  last_occurrence?: ScheduleOccurrence | null;
  deliveries?: ScheduleDelivery[];
}
