/**
 * MobileScheduleDetail — 定时任务移动端详情（开发执行报告 §30）：
 * Full Screen（执行记录可能很多），← 标题在 Drawer 头部，正文按
 * 状态 / 执行计划 / 任务内容 / 通知 / 执行记录分节。
 *
 * 任务配置与执行记录是两个失败域（三次复审 §36–§37）：历史接口故障时
 * 配置照常显示，执行记录区域给独立错误 + 重试；执行记录分页（§35），
 * 第 51 条以前的历史不再不可见。
 */
import React from 'react';
import { Alert, Button, Skeleton, Tag } from 'antd';
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
  const {
    schedule, loading, error,
    occurrences, occurrencesLoading, occurrenceError,
    hasMoreOccurrences, loadingMoreOccurrences, loadMoreOccurrences,
    retryOccurrences, retrySchedule,
  } = useScheduleDetail(open, scheduleId);

  return (
    <MobileFullScreenDrawer
      open={open}
      title={schedule?.name ?? '定时任务详情'}
      onClose={onClose}
    >
      {loading && <Skeleton active />}
      {error && (
        /* 配置失败可原地重试（四次复审 P2-3），不再只能关抽屉重开。 */
        <Alert
          type="error"
          showIcon
          message="加载任务详情失败"
          description={error}
          action={<Button size="small" onClick={() => void retrySchedule()}>重试</Button>}
        />
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

          {/* 执行记录：独立失败域（§37）——历史接口挂了不影响任务配置。 */}
          <MobileSection title="执行记录" flush>
            {occurrenceError && occurrences.length === 0 ? (
              <div className="mobile-console-empty">
                <strong>加载执行记录失败</strong>
                <Button onClick={() => void retryOccurrences()}>重试</Button>
              </div>
            ) : occurrencesLoading && occurrences.length === 0 ? (
              <Skeleton active title={false} paragraph={{ rows: 3 }} />
            ) : occurrences.length === 0 ? (
              <div className="mobile-console-empty">还没有执行记录</div>
            ) : (
              <>
                {occurrences.map((occ) => (
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
                ))}
                {/* 唯一 CTA：翻页失败 → 重试；否则加载更多（§35）。 */}
                {occurrenceError ? (
                  <button
                    type="button"
                    className="mobile-console-more"
                    onClick={() => void loadMoreOccurrences()}
                  >
                    {loadingMoreOccurrences ? '加载中…' : '加载失败，点击重试'}
                  </button>
                ) : hasMoreOccurrences && (
                  <button
                    type="button"
                    className="mobile-console-more"
                    onClick={() => void loadMoreOccurrences()}
                  >
                    {loadingMoreOccurrences ? '加载中…' : '加载更多执行记录'}
                  </button>
                )}
              </>
            )}
          </MobileSection>
        </>
      )}
    </MobileFullScreenDrawer>
  );
}
