/**
 * MobileSyncPage — 移动端同步管理（开发执行报告 §47/§48）：
 * 最近同步状态卡 + 立即同步 + vertical 表单 + 同步记录列表。
 *
 * 状态机（二次复审 P1-3/P2-8）：
 *   · config 与 runs 是独立失败域（Promise.allSettled）——历史记录接口
 *     挂了不能连累同步配置管理；
 *   · 配置未加载成功时绝不渲染可保存的 Form：initialValues 本身就是
 *     前端默认值，允许保存等于把默认值写成真正的企业同步配置；
 *   · 已有配置后的刷新失败 = stale warning（旧数据保留 + 提示），不是
 *     无声吞掉。
 */
import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  Alert,
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
  const [configError, setConfigError] = useState<string | null>(null);
  const [runsError, setRunsError] = useState<string | null>(null);
  const [form] = Form.useForm();
  const [saving, setSaving] = useState(false);
  // sequence guard（三次复审 §52–§53）：页面有多个重试入口，快速连点会
  // 触发多个 load；响应乱序返回时旧数据会覆盖新数据。
  const loadSeqRef = useRef(0);
  const [refreshing, setRefreshing] = useState(false);

  // 失败保留旧数据（cfg/runs 不清空），显式错误态 + 重试；config 与 runs
  // 独立失败域（P2-8）——syncRuns 挂了不能让配置管理整体不可用。
  const load = useCallback(async () => {
    const seq = ++loadSeqRef.current;
    setRefreshing(true);
    setConfigError(null);
    setRunsError(null);
    const [configResult, runsResult] = await Promise.allSettled([
      enterpriseApi.syncConfig(),
      enterpriseApi.syncRuns(),
    ]);
    // 旧一代 load 的响应（成功或失败）一律作废，只有最新一代能落地。
    if (seq !== loadSeqRef.current) return;
    if (configResult.status === 'fulfilled') {
      const loaded = configResult.value;
      setCfg(loaded);
      form.setFieldsValue({ ...loaded, daily_time: dayjs(loaded.daily_time, 'HH:mm') });
    } else {
      setConfigError('加载同步配置失败');
    }
    if (runsResult.status === 'fulfilled') {
      setRuns(runsResult.value);
    } else {
      setRunsError('加载同步记录失败');
    }
    setRefreshing(false);
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
        {cfg === null && configError ? (
          <div className="mobile-console-empty">
            <strong>加载失败</strong>
            {configError}
            <Button loading={refreshing} onClick={() => void load()}>重试</Button>
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
        {cfg === null ? (
          // 配置没加载成功绝不渲染可保存的 Form（P1-3）：initialValues 是
          // 前端默认值，不是服务端配置，允许保存就可能把默认值写回服务器。
          configError ? (
            <div className="mobile-console-empty">
              <strong>无法加载同步配置</strong>
              {configError}
              <Button loading={refreshing} onClick={() => void load()}>重试</Button>
            </div>
          ) : (
            <Skeleton active />
          )
        ) : (
          <>
            {/* 已有配置后的刷新失败 = stale warning，不是无声吞掉（P2-7）。 */}
            {configError && (
              <Alert
                type="warning"
                showIcon
                message="刷新失败，当前显示的是上次已加载数据"
                style={{ marginBottom: 12 }}
                action={(
                  <Button
                    size="small"
                    loading={refreshing}
                    onClick={() => void load()}
                  >
                    重试
                  </Button>
                )}
              />
            )}
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
          </>
        )}
      </MobileSection>

      <MobileSection title="同步记录" flush>
        {runs.length === 0 ? (
          runsError ? (
            <div className="mobile-console-empty">
              {runsError}
              <Button loading={refreshing} onClick={() => void load()}>重试</Button>
            </div>
          ) : (
            <div className="mobile-console-empty">还没有同步记录</div>
          )
        ) : (
          <>
            {runsError && (
              <div className="mobile-console-empty">
                刷新记录失败，以上为上次已加载数据
                <Button
                  size="small"
                  loading={refreshing}
                  onClick={() => void load()}
                >
                  重试
                </Button>
              </div>
            )}
            {runs.map((run) => (
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
          </>
        )}
      </MobileSection>
    </MobilePage>
  );
}
