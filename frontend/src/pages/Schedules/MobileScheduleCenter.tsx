/**
 * MobileScheduleCenter — 定时任务移动端 (开发执行报告 §23–§30)。
 *
 * 与桌面共用 useSchedules（业务层绝不复制），信息架构换成移动版：
 *   · 顶栏：Shell Header（定时任务 + ＋），＋ 直接打开编辑器（§24）；
 *   · 工具区：状态 Segmented + 搜索，无独立刷新按钮，错误态给重试；
 *   · 卡片：名称 + compact 状态 + 计划/下次执行，点击进全屏详情；
 *   •••    ：ActionSheet 收纳 立即运行/编辑/启停/记录/删除（§27），
 *            删除走 Modal.confirm（手机上比 Popconfirm 稳）。
 */
import React, { useCallback, useState } from 'react';
import { Alert, Button, Modal, Segmented, Skeleton } from 'antd';
import {
  CaretRightOutlined,
  DeleteOutlined,
  EditOutlined,
  EyeOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
} from '@ant-design/icons';
import { useSchedules } from '@/hooks/useSchedules';
import { useMobileHeader } from '@/shell/mobileHeader';
import type { Schedule, ScheduleStatusFilter } from '@/types/schedule';
import { ScheduleStatusTag } from '@/components/Schedules/ScheduleStatusTag';
import { MobileScheduleEditor } from '@/components/Schedules/MobileScheduleEditor';
import { MobileScheduleDetail } from '@/components/Schedules/MobileScheduleDetail';
import {
  MobileActionSheet,
  MobileEmptyState,
  MobilePage,
  MobileSearchBar,
  type MobileAction,
} from '@/components/MobileConsole';
import { describeSchedulePlan, formatDateTime } from '@/lib/scheduleFormat';
import './SchedulesPage.css';

function ScheduleCardSkeleton() {
  return (
    <div className="mobile-schedule-card">
      <Skeleton active title paragraph={{ rows: 2 }} />
    </div>
  );
}

export function MobileScheduleCenter() {
  const {
    data, loading, error, status, search, mutatingId,
    setStatus, setSearch, reload, toggleEnabled, runNow, remove,
  } = useSchedules();
  const [editorOpen, setEditorOpen] = useState(false);
  const [editing, setEditing] = useState<Schedule | null>(null);
  const [detailId, setDetailId] = useState<number | null>(null);
  const [sheetFor, setSheetFor] = useState<Schedule | null>(null);

  // 顶栏 ＝ 新建定时任务（§24）：直接打开编辑器，不再 navigate('/')。
  const openNew = useCallback(() => {
    setEditing(null);
    setEditorOpen(true);
  }, []);
  useMobileHeader({ onAction: openNew });

  const openEdit = (s: Schedule) => {
    setEditing(s);
    setEditorOpen(true);
  };

  const confirmRemove = (s: Schedule) => {
    Modal.confirm({
      title: '删除定时任务？',
      content: '历史执行记录会保留。',
      okText: '删除',
      okButtonProps: { danger: true },
      cancelText: '取消',
      onOk: () => remove(s.id),
    });
  };

  const actions: MobileAction[] = sheetFor ? [
    {
      key: 'run', label: '立即运行', icon: <CaretRightOutlined />,
      disabled: mutatingId === sheetFor.id,
      onClick: () => void runNow(sheetFor.id),
    },
    {
      key: 'edit', label: '编辑', icon: <EditOutlined />,
      onClick: () => openEdit(sheetFor),
    },
    {
      key: 'toggle',
      label: sheetFor.enabled ? '暂停' : '启用',
      icon: sheetFor.enabled ? <PauseCircleOutlined /> : <PlayCircleOutlined />,
      disabled: mutatingId === sheetFor.id,
      onClick: () => void toggleEnabled(sheetFor.id, !sheetFor.enabled),
    },
    {
      key: 'detail', label: '查看执行记录', icon: <EyeOutlined />,
      onClick: () => setDetailId(sheetFor.id),
    },
    {
      key: 'remove', label: '删除', icon: <DeleteOutlined />, danger: true,
      onClick: () => confirmRemove(sheetFor),
    },
  ] : [];

  return (
    <MobilePage>
      <div className="mobile-console-page__sticky">
        <Segmented
          block
          value={status}
          onChange={(v) => setStatus(v as ScheduleStatusFilter)}
          options={[
            { value: 'all', label: '全部' },
            { value: 'running', label: '运行中' },
            { value: 'paused', label: '已暂停' },
            { value: 'failed', label: '失败' },
          ]}
        />
        <MobileSearchBar
          placeholder="搜索任务名称"
          value={search}
          onChange={setSearch}
        />
      </div>

      {error && (
        <Alert
          type="error"
          showIcon
          message="加载定时任务失败"
          description={error}
          action={<Button size="small" onClick={() => void reload()}>重试</Button>}
          style={{ marginTop: 12 }}
        />
      )}

      {!error && (loading && data.length === 0 ? (
        <div className="mobile-schedule-list">
          <ScheduleCardSkeleton />
          <ScheduleCardSkeleton />
          <ScheduleCardSkeleton />
        </div>
      ) : data.length === 0 ? (
        status === 'all' && !search ? (
          <MobileEmptyState
            title="还没有定时任务"
            hint="创建一个任务，让智能体自动完成重复工作"
            action={(
              <Button type="primary" onClick={openNew}>
                创建定时任务
              </Button>
            )}
          />
        ) : (
          <MobileEmptyState title="没有匹配当前条件的任务" />
        )
      ) : (
        <div className="mobile-schedule-list">
          {data.map((s) => (
            <div key={s.id} className="mobile-schedule-card">
              {/* Wrapper / Main / ••• — 全部原生 button，浏览器接管焦点树与
                  Enter/Space 激活（二次复审 P2-2，不再用 role=button 容器）。 */}
              <button
                type="button"
                className="mobile-schedule-card__main"
                aria-label={`查看任务详情：${s.name}`}
                onClick={() => setDetailId(s.id)}
              >
                <div className="mobile-schedule-card__row">
                  <span className="mobile-schedule-card__name">{s.name}</span>
                  <ScheduleStatusTag schedule={s} compact />
                </div>
                <div className="mobile-schedule-card__meta">
                  <span>{describeSchedulePlan(s)}</span>
                  <span>
                    下次执行：
                    <time dateTime={s.next_run_at ?? undefined}>
                      {formatDateTime(s.next_run_at)}
                    </time>
                  </span>
                </div>
              </button>
              <button
                type="button"
                className="mobile-schedule-card__more"
                aria-label={`更多操作：${s.name}`}
                onClick={() => setSheetFor(s)}
              >
                •••
              </button>
            </div>
          ))}
        </div>
      ))}

      <MobileActionSheet
        open={sheetFor !== null}
        title={sheetFor?.name}
        actions={actions}
        onClose={() => setSheetFor(null)}
      />

      <MobileScheduleEditor
        open={editorOpen}
        editing={editing}
        onClose={() => setEditorOpen(false)}
        onSaved={() => void reload()}
      />
      <MobileScheduleDetail
        open={detailId !== null}
        scheduleId={detailId}
        onClose={() => setDetailId(null)}
      />
    </MobilePage>
  );
}
