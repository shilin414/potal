/**
 * SchedulesPage — 定时任务中心（Console 页面）。
 * 桌面表格 / 移动端单列卡片；空、错、加载三态齐全。
 */
import React, { useMemo, useState } from 'react';
import {
  Alert,
  Button,
  Empty,
  Input,
  Popconfirm,
  Segmented,
  Space,
  Spin,
  Switch,
  Table,
} from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { useSchedules } from '@/hooks/useSchedules';
import { ScheduleEditorModal } from '@/components/Schedules/ScheduleEditorModal';
import { ScheduleDetailDrawer } from '@/components/Schedules/ScheduleDetailDrawer';
import { ScheduleStatusTag } from '@/components/Schedules/ScheduleStatusTag';
import { describeSchedulePlan, formatDateTime } from '@/lib/scheduleFormat';
import type { Schedule, ScheduleStatusFilter } from '@/types/schedule';
import './SchedulesPage.css';

export function SchedulesPage() {
  const {
    data, loading, error, status, search, mutatingId,
    setStatus, setSearch, reload, toggleEnabled, runNow, remove,
  } = useSchedules();
  const [editorOpen, setEditorOpen] = useState(false);
  const [editing, setEditing] = useState<Schedule | null>(null);
  const [detailId, setDetailId] = useState<number | null>(null);

  const openNew = () => {
    setEditing(null);
    setEditorOpen(true);
  };
  const openEdit = (s: Schedule) => {
    setEditing(s);
    setEditorOpen(true);
  };

  const columns = [
    {
      title: '名称',
      dataIndex: 'name',
      key: 'name',
      render: (_: unknown, s: Schedule) => (
        <div>
          <div className="schedule-card-name">{s.name}</div>
          <span className="schedule-detail-meta">{s.prompt.slice(0, 40)}{s.prompt.length > 40 ? '…' : ''}</span>
        </div>
      ),
    },
    {
      title: '执行计划',
      key: 'plan',
      render: (_: unknown, s: Schedule) => describeSchedulePlan(s),
    },
    {
      title: '下次执行',
      dataIndex: 'next_run_at',
      key: 'next_run_at',
      render: (v: string | null) => (
        <time dateTime={v ?? undefined}>{formatDateTime(v)}</time>
      ),
    },
    {
      title: '状态',
      key: 'status',
      render: (_: unknown, s: Schedule) => <ScheduleStatusTag schedule={s} />,
    },
    {
      title: '启用',
      dataIndex: 'enabled',
      key: 'enabled',
      render: (_: unknown, s: Schedule) => (
        <Switch
          checked={s.enabled}
          loading={mutatingId === s.id}
          onChange={(checked) => void toggleEnabled(s.id, checked)}
          aria-label={`${s.enabled ? '停用' : '启用'} ${s.name}`}
        />
      ),
    },
    {
      title: '操作',
      key: 'actions',
      render: (_: unknown, s: Schedule) => (
        <Space size="small">
          <Button size="small" type="link" onClick={() => setDetailId(s.id)}>详情</Button>
          <Button
            size="small"
            type="link"
            loading={mutatingId === s.id}
            onClick={() => void runNow(s.id)}
          >
            立即运行
          </Button>
          <Button size="small" type="link" onClick={() => openEdit(s)}>编辑</Button>
          <Popconfirm
            title="删除定时任务？"
            description="历史执行记录会保留。"
            okText="删除"
            cancelText="取消"
            onConfirm={async () => {
              await remove(s.id);
            }}
          >
            <Button size="small" type="link" danger loading={mutatingId === s.id}>删除</Button>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <div className="schedules-page">
      <div className="page-header">
        <h1 className="page-title">定时任务</h1>
        <p className="page-subtitle">让智能体按计划自动执行重复工作，完成后可发送飞书消息</p>
      </div>

      <div className="schedules-toolbar">
        <div className="schedules-toolbar-left">
          <Segmented
            value={status}
            onChange={(v) => setStatus(v as ScheduleStatusFilter)}
            options={[
              { value: 'all', label: '全部' },
              { value: 'running', label: '运行中' },
              { value: 'paused', label: '已暂停' },
              { value: 'failed', label: '失败任务' },
            ]}
          />
          <Input.Search
            placeholder="搜索任务名称"
            allowClear
            style={{ maxWidth: 240 }}
            onSearch={setSearch}
            onChange={(e) => {
              if (!e.target.value) setSearch('');
            }}
          />
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => void reload()} aria-label="刷新列表" />
          <Button type="primary" icon={<PlusOutlined />} onClick={openNew}>
            新建定时任务
          </Button>
        </Space>
      </div>

      {error && (
        <Alert
          type="error"
          showIcon
          message="加载定时任务失败"
          description={error}
          action={<Button size="small" onClick={() => void reload()}>重试</Button>}
          style={{ marginBottom: 16 }}
        />
      )}

      {!error && (loading ? (
        <div style={{ padding: '48px 0', textAlign: 'center' }} aria-busy="true">
          <Spin size="large" />
        </div>
      ) : data.length === 0 ? (
        <Empty
          description={
            status === 'all' && !search ? (
              <div className="schedule-empty-extra">
                <p style={{ fontWeight: 600 }}>还没有定时任务</p>
                <p>创建一个任务，让智能体按固定时间自动工作</p>
              </div>
            ) : (
              '没有匹配当前条件的任务'
            )
          }
        >
          {status === 'all' && !search && (
            <Button type="primary" icon={<PlusOutlined />} onClick={openNew}>
              新建定时任务
            </Button>
          )}
        </Empty>
      ) : (
        <>
          {/* 桌面表格 */}
          <div className="schedules-desktop-table">
            <Table
              rowKey="id"
              columns={columns}
              dataSource={data}
              loading={loading}
              pagination={false}
              scroll={{ x: 860 }}
            />
          </div>
          {/* 移动端卡片 */}
          <div className="schedules-mobile-cards">
            {data.map((s) => (
              <div key={s.id} className="schedule-card" onClick={() => setDetailId(s.id)} role="button" tabIndex={0}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    setDetailId(s.id);
                  }
                }}
              >
                <div className="schedule-card-row">
                  <span className="schedule-card-name">{s.name}</span>
                  <ScheduleStatusTag schedule={s} />
                </div>
                <div className="schedule-card-meta">
                  <span>{describeSchedulePlan(s)}</span>
                  <span>下次执行：
                    <time dateTime={s.next_run_at ?? undefined}>{formatDateTime(s.next_run_at)}</time>
                  </span>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginTop: 12 }}>
                  <Switch
                    checked={s.enabled}
                    size="small"
                    loading={mutatingId === s.id}
                    onClick={(checked) => void toggleEnabled(s.id, checked)}
                    aria-label={`${s.enabled ? '停用' : '启用'} ${s.name}`}
                  />
                  <Button
                    size="small"
                    onClick={(e) => {
                      e.stopPropagation();
                      void runNow(s.id);
                    }}
                  >
                    立即运行
                  </Button>
                  <Button size="small" type="link" onClick={(e) => { e.stopPropagation(); openEdit(s); }}>
                    编辑
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </>
      ))}

      <ScheduleEditorModal
        open={editorOpen}
        editing={editing}
        onClose={() => setEditorOpen(false)}
        onSaved={() => void reload()}
      />
      <ScheduleDetailDrawer
        open={detailId !== null}
        scheduleId={detailId}
        onClose={() => setDetailId(null)}
      />
    </div>
  );
}
