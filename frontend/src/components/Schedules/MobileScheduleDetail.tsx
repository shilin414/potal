/**
 * MobileScheduleDetail — 定时任务移动端详情（开发执行报告 §30）：
 * Full Screen（执行记录可能很多），← 标题在 Drawer 头部，正文按
 * 状态 / 执行计划 / 任务内容 / 通知 / 执行记录分节。
 */
import React from 'react';
import { Alert, Skeleton, Tag } from 'antd';
import { MobileFullScreenDrawer, MobileSection } from '@/components/MobileConsole';
import { OccurrenceStatusTag, ScheduleStatusTag } from './ScheduleStatusTag';
import { describeSchedulePlan, formatDateTime } from '@/lib/scheduleFormat';
import { useScheduleDetail } from './useScheduleDetail';
import './MobileScheduleDetail.css';

export interface MobileScheduleDetailProps {
  open: boolean;
  scheduleId: number | null;
  onClose: () => void;
}

export function MobileScheduleDetail({ open, scheduleId, onClose }: MobileScheduleDetailProps) {
  const { schedule, occurrences, loading, error } = useScheduleDetail(open, scheduleId);

  return (
    <MobileFullScreenDrawer
      open={open}
      title={schedule?.name ?? '定时任务详情'}
      onClose={onClose}
    >
      {loading && <Skeleton active />}
      {error && (
        <Alert type="error" showIcon message="加载失败" description={error} />
      )}
      {!loading && !error && schedule && (
        <>
          <div className="mobile-schedule-detail__status">
            <ScheduleStatusTag schedule={schedule} />
          </div>

          <MobileSection title="执行计划">
            <div className="mobile-schedule-detail__kv">
              <span>{describeSchedulePlan(schedule)}</span>
              <span>
                下次执行：
                <time dateTime={schedule.next_run_at ?? undefined}>
                  {formatDateTime(schedule.next_run_at)}
                </time>
              </span>
            </div>
          </MobileSection>

          <MobileSection title="任务内容">
            <p className="mobile-schedule-detail__prompt">{schedule.prompt}</p>
          </MobileSection>

          {schedule.deliveries && schedule.deliveries.length > 0 && (
            <MobileSection title="通知">
              <div className="mobile-schedule-detail__kv">
                {schedule.deliveries.map((d) => (
                  <Tag key={d.id}>{d.target_name || d.target_id}</Tag>
                ))}
              </div>
            </MobileSection>
          )}

          <MobileSection title="执行记录" flush>
            {occurrences.length === 0 ? (
              <div className="mobile-console-empty">还没有执行记录</div>
            ) : (
              occurrences.map((occ) => (
                <div key={occ.id} className="mobile-schedule-detail__occ">
                  <div className="mobile-schedule-detail__occ-head">
                    <OccurrenceStatusTag status={occ.status} />
                    <time dateTime={occ.scheduled_at}>{formatDateTime(occ.scheduled_at)}</time>
                  </div>
                  {(occ.enqueued_at || occ.finished_at) && (
                    <div className="mobile-schedule-detail__occ-meta">
                      {occ.enqueued_at && <span>入队 {formatDateTime(occ.enqueued_at)}</span>}
                      {occ.finished_at && <span>完成 {formatDateTime(occ.finished_at)}</span>}
                    </div>
                  )}
                </div>
              ))
            )}
          </MobileSection>
        </>
      )}
    </MobileFullScreenDrawer>
  );
}
