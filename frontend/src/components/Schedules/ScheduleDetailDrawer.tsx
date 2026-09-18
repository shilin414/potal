/**
 * ScheduleDetailDrawer — 配置摘要 + 执行历史（Desktop Drawer，外观不变）。
 * 加载逻辑拆到 useScheduleDetail 与移动端 MobileScheduleDetail 共用。
 */
import React from 'react';
import { Alert, Drawer, Empty, Skeleton, Tag, Typography } from 'antd';
import type { ScheduleOccurrence } from '@/types/schedule';
import { OccurrenceStatusTag } from '@/components/Schedules/ScheduleStatusTag';
import { describeSchedulePlan, formatDateTime } from '@/lib/scheduleFormat';
import { useScheduleDetail } from './useScheduleDetail';

const { Text } = Typography;

export interface ScheduleDetailDrawerProps {
  open: boolean;
  scheduleId: number | null;
  onClose: () => void;
}

export function ScheduleDetailDrawer({ open, scheduleId, onClose }: ScheduleDetailDrawerProps) {
  const { schedule, occurrences, loading, error } = useScheduleDetail(open, scheduleId);

  return (
    <Drawer
      title={schedule?.name ?? '定时任务详情'}
      open={open}
      onClose={onClose}
      width={480}
    >
      {loading && <Skeleton active />}
      {error && (
        <Alert type="error" showIcon message="加载失败" description={error} />
      )}
      {!loading && !error && schedule && (
        <>
          <div className="schedule-detail-meta">
            <p>
              <Text type="secondary">执行计划：</Text>
              {describeSchedulePlan(schedule)}
            </p>
            <p>
              <Text type="secondary">下次执行：</Text>
              <time dateTime={schedule.next_run_at ?? undefined}>
                {formatDateTime(schedule.next_run_at)}
              </time>
            </p>
            <p>
              <Text type="secondary">任务指令：</Text>
              {schedule.prompt}
            </p>
            {schedule.deliveries && schedule.deliveries.length > 0 && (
              <p>
                <Text type="secondary">飞书投递：</Text>
                {schedule.deliveries.map((d) => (
                  <Tag key={d.id}>{d.target_name || d.target_id}</Tag>
                ))}
              </p>
            )}
          </div>

          <Typography.Title level={5} style={{ marginTop: 20 }}>
            最近执行
          </Typography.Title>
          {occurrences.length === 0 ? (
            <Empty description="还没有执行记录" image={Empty.PRESENTED_IMAGE_SIMPLE} />
          ) : (
            occurrences.map((occ: ScheduleOccurrence) => (
              <div key={occ.id} className="schedule-card">
                <div className="schedule-card-row">
                  <OccurrenceStatusTag status={occ.status} />
                  <span className="schedule-detail-meta">
                    <time dateTime={occ.scheduled_at}>{formatDateTime(occ.scheduled_at)}</time>
                  </span>
                </div>
                <div className="schedule-card-meta">
                  {occ.enqueued_at && <span>入队：{formatDateTime(occ.enqueued_at)}</span>}
                  {occ.finished_at && <span>完成：{formatDateTime(occ.finished_at)}</span>}
                  {occ.run_id && (
                    <span>
                      运行记录：
                      <Text code style={{ fontSize: 12 }}>{occ.run_id.slice(0, 8)}</Text>
                    </span>
                  )}
                </div>
              </div>
            ))
          )}
        </>
      )}
    </Drawer>
  );
}
