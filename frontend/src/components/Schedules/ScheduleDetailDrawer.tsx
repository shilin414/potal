/**
 * ScheduleDetailDrawer — 配置摘要 + 执行历史（Desktop Drawer，外观不变）。
 * 加载逻辑拆到 useScheduleDetail 与移动端 MobileScheduleDetail 共用。
 *
 * 任务配置与执行记录是两个失败域（三次复审 §36–§37）：历史接口故障时
 * 配置照常显示，最近执行区域给独立错误 + 重试；执行记录分页（§35）。
 */
import React from 'react';
import { Alert, Button, Drawer, Empty, Skeleton, Tag, Typography } from 'antd';
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
  const {
    schedule, loading, error,
    occurrences, occurrencesLoading, occurrenceError,
    hasMoreOccurrences, loadingMoreOccurrences, loadMoreOccurrences,
    retryOccurrences,
  } = useScheduleDetail(open, scheduleId);

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

          {/* 执行记录：独立失败域（§37）——历史接口挂了不影响任务配置。 */}
          <Typography.Title level={5} style={{ marginTop: 20 }}>
            最近执行
          </Typography.Title>
          {occurrenceError && occurrences.length === 0 ? (
            <div style={{ textAlign: 'center', padding: '12px 0' }}>
              <Alert
                type="error"
                showIcon
                message="加载执行记录失败"
                description={occurrenceError}
                action={<Button size="small" onClick={() => void retryOccurrences()}>重试</Button>}
              />
            </div>
          ) : occurrencesLoading && occurrences.length === 0 ? (
            <Skeleton active title={false} paragraph={{ rows: 3 }} />
          ) : occurrences.length === 0 ? (
            <Empty description="还没有执行记录" image={Empty.PRESENTED_IMAGE_SIMPLE} />
          ) : (
            <>
              {occurrences.map((occ: ScheduleOccurrence) => (
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
              ))}
              {/* 唯一 CTA：翻页失败 → 重试；否则加载更多（§35）。 */}
              {occurrenceError ? (
                <div style={{ textAlign: 'center', marginTop: 12 }}>
                  <Button
                    danger
                    loading={loadingMoreOccurrences}
                    onClick={() => void loadMoreOccurrences()}
                  >
                    加载失败，点击重试
                  </Button>
                </div>
              ) : hasMoreOccurrences && (
                <div style={{ textAlign: 'center', marginTop: 12 }}>
                  <Button loading={loadingMoreOccurrences} onClick={() => void loadMoreOccurrences()}>
                    加载更多
                  </Button>
                </div>
              )}
            </>
          )}
        </>
      )}
    </Drawer>
  );
}
