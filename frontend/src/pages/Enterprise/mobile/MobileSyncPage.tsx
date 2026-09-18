/**
 * MobileSyncPage — 移动端同步管理（开发执行报告 §47/§48）：
 * 最近同步状态卡 + 立即同步 + vertical 表单 + 同步记录列表。
 */
import React, { useCallback, useEffect, useState } from 'react';
import {
  Button,
  Form,
  Input,
  InputNumber,
  Select,
  Skeleton,
  Switch,
  Tag,
  TimePicker,
  message,
} from 'antd';
import dayjs from 'dayjs';
import { enterpriseApi, type SyncConfig, type SyncRun } from '../enterpriseApi';
import { fmt } from '../enterpriseNav';
import {
  MobilePage,
  MobileSection,
} from '@/components/MobileConsole';
import '../EnterpriseMobile.css';

const STATUS_TAG: Record<SyncRun['status'], { color: string; label: string }> = {
  pending: { color: 'default', label: '等待中' },
  running: { color: 'processing', label: '进行中' },
  success: { color: 'success', label: '成功' },
  failed: { color: 'error', label: '失败' },
};

export default function MobileSyncPage() {
  const [cfg, setCfg] = useState<SyncConfig | null>(null);
  const [runs, setRuns] = useState<SyncRun[]>([]);
  const [syncing, setSyncing] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [form] = Form.useForm();
  const [saving, setSaving] = useState(false);

  // 失败保留旧数据（cfg/runs 不清空），显式错误态 + 重试（二次复审 P2-8）；
  // 不再让 Promise 错误静默逃逸成 unhandled rejection。
  const load = useCallback(async () => {
    setLoadError(null);
    try {
      const [c, r] = await Promise.all([
        enterpriseApi.syncConfig(),
        enterpriseApi.syncRuns(),
      ]);
      setCfg(c);
      setRuns(r);
      form.setFieldsValue({ ...c, daily_time: dayjs(c.daily_time, 'HH:mm') });
    } catch {
      setLoadError('加载同步信息失败');
    }
  }, [form]);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async () => {
    const v = await form.validateFields();
    setSaving(true);
    try {
      const next = await enterpriseApi.updateSyncConfig({
        ...v,
        daily_time: v.daily_time.format('HH:mm'),
      });
      setCfg(next);
      message.success('同步设置已保存');
    } catch {
      message.error('保存同步设置失败');
    } finally {
      setSaving(false);
    }
  };

  const trigger = async () => {
    setSyncing(true);
    try {
      await enterpriseApi.triggerSync();
      message.success('同步任务已进入队列');
      await load();
    } catch {
      message.error('发起同步失败');
    } finally {
      setSyncing(false);
    }
  };

  const last = runs[0];

  return (
    <MobilePage>
      <MobileSection title="最近同步">
        {cfg === null && loadError ? (
          <div className="mobile-console-empty">
            <strong>加载失败</strong>
            {loadError}
            <Button onClick={() => void load()}>重试</Button>
          </div>
        ) : cfg === null ? (
          <Skeleton active />
        ) : (
          <div className="mobile-sync__status">
            <div className="mobile-sync__status-line">
              {last && (
                <Tag color={STATUS_TAG[last.status].color}>
                  {STATUS_TAG[last.status].label}
                </Tag>
              )}
              <span>{fmt(cfg.last_success_at)}</span>
            </div>
            <div className="mobile-sync__status-line mobile-sync__status-line--dim">
              下一次：{fmt(cfg.next_run_at)}
            </div>
            {last?.status === 'failed' && last.error_message && (
              <div className="mobile-sync__status-line mobile-sync__status-line--dim">
                {last.error_message}
              </div>
            )}
          </div>
        )}
        <Button
          type="primary"
          block
          loading={syncing}
          className="mobile-sync__trigger"
          onClick={() => void trigger()}
        >
          立即同步
        </Button>
      </MobileSection>

      <MobileSection title="自动同步">
        {/* vertical，每字段一行（§48）——不用桌面 inline form */}
        <Form
          form={form}
          layout="vertical"
          initialValues={{
            enabled: false,
            schedule_type: 'interval',
            interval_minutes: 360,
            daily_time: dayjs('02:00', 'HH:mm'),
            timezone: 'Asia/Shanghai',
          }}
        >
          <Form.Item name="enabled" valuePropName="checked" label="启用">
            <Switch />
          </Form.Item>
          <Form.Item name="schedule_type" label="同步方式">
            <Select
              options={[
                { value: 'interval', label: '按间隔' },
                { value: 'daily', label: '每天' },
              ]}
            />
          </Form.Item>
          <Form.Item noStyle shouldUpdate>
            {({ getFieldValue }) => (
              getFieldValue('schedule_type') === 'daily' ? (
                <Form.Item name="daily_time" label="执行时间">
                  <TimePicker format="HH:mm" style={{ width: '100%' }} />
                </Form.Item>
              ) : (
                <Form.Item name="interval_minutes" label="间隔（分钟）">
                  <InputNumber min={15} max={10080} style={{ width: '100%' }} />
                </Form.Item>
              )
            )}
          </Form.Item>
          <Form.Item name="timezone" label="时区">
            <Input />
          </Form.Item>
          <Button type="primary" block loading={saving} onClick={() => void save()}>
            保存设置
          </Button>
        </Form>
      </MobileSection>

      <MobileSection title="同步记录" flush>
        {runs.length === 0 ? (
          <div className="mobile-console-empty">还没有同步记录</div>
        ) : runs.map((run) => (
          <div key={run.id} className="mobile-sync__run">
            <div className="mobile-sync__run-head">
              <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <Tag color={STATUS_TAG[run.status].color}>
                  {STATUS_TAG[run.status].label}
                </Tag>
                <span style={{ fontSize: 13, color: 'var(--color-text-sec)' }}>
                  {run.trigger_type === 'manual' ? '手动' : '自动'}
                </span>
              </span>
              <time dateTime={run.created_at}>{fmt(run.created_at)}</time>
            </div>
            <div className="mobile-sync__run-meta">
              <span>部门 {run.departments_count}</span>
              <span>员工 {run.active_users_count} / {run.users_count}</span>
              <span>关系 {run.active_memberships_count} / {run.memberships_count}</span>
            </div>
            {run.status === 'failed' && run.error_message && (
              <div className="mobile-sync__run-meta" style={{ color: 'var(--color-error)' }}>
                {run.error_message}
              </div>
            )}
          </div>
        ))}
      </MobileSection>
    </MobilePage>
  );
}
