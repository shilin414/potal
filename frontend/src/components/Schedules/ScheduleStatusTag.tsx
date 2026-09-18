/** 定时任务状态 Tag：状态始终有文字（不只靠颜色）。compact 供移动卡片用（§26）。 */
import React from 'react';
import { Tag } from 'antd';
import {
  CheckCircleOutlined,
  ClockCircleOutlined,
  CloseCircleOutlined,
  MinusCircleOutlined,
  PauseCircleOutlined,
  SyncOutlined,
  ExclamationCircleOutlined,
} from '@ant-design/icons';
import type { OccurrenceStatus } from '@/types/schedule';

export type ScheduleCardStatus = 'running' | 'paused' | 'failed';

const OCCURRENCE_STATUS: Record<OccurrenceStatus, { color: string; icon: React.ReactNode; label: string }> = {
  pending: { color: 'default', icon: <ClockCircleOutlined />, label: '等待执行' },
  queued: { color: 'processing', icon: <ClockCircleOutlined />, label: '排队中' },
  running: { color: 'processing', icon: <SyncOutlined spin />, label: '执行中' },
  succeeded: { color: 'success', icon: <CheckCircleOutlined />, label: '成功' },
  failed: { color: 'error', icon: <CloseCircleOutlined />, label: '失败' },
  skipped: { color: 'warning', icon: <MinusCircleOutlined />, label: '已跳过' },
};

export function OccurrenceStatusTag({ status }: { status: OccurrenceStatus }) {
  const s = OCCURRENCE_STATUS[status] ?? OCCURRENCE_STATUS.pending;
  return (
    <Tag color={s.color} icon={s.icon}>
      {s.label}
    </Tag>
  );
}

/**
 * 列表卡片状态：运行中 / 已暂停 / 上次失败。
 * `compact`（移动卡片，§26）：去图标、小字号，只留颜色 + 文字。
 */
export function ScheduleStatusTag({
  schedule, compact = false,
}: {
  schedule: { enabled: boolean; last_occurrence?: { status: OccurrenceStatus } | null };
  compact?: boolean;
}) {
  if (!schedule.enabled) {
    return (
      <Tag icon={compact ? undefined : <PauseCircleOutlined />}>已暂停</Tag>
    );
  }
  if (schedule.last_occurrence?.status === 'failed') {
    return (
      <Tag color="error" icon={compact ? undefined : <ExclamationCircleOutlined />}>上次失败</Tag>
    );
  }
  return (
    <Tag color="success" icon={compact ? undefined : <CheckCircleOutlined />}>运行中</Tag>
  );
}
