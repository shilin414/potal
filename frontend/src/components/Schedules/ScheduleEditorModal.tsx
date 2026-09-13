/**
 * ScheduleEditorModal — 新建/编辑定时任务。
 * 三区块结构化表单：基本信息 → 执行时间 → 策略与会话（+ 飞书投递）。
 * 与 AgentEditorModal 同构：异步初始化、确认 loading、字段级错误回显。
 */
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Alert,
  Checkbox,
  DatePicker,
  Form,
  Input,
  InputNumber,
  Modal,
  Radio,
  Select,
  Switch,
  message,
} from 'antd';
import { fetchV2Applications } from '@/services/runApi';
import { fetchFeishuTargets } from '@/services/shareApi';
import { createSchedule, previewScheduleRuns, updateSchedule } from '@/services/scheduleApi';
import type { V2Application } from '@/services/runApi';
import type { FeishuForwardTarget } from '@/services/shareApi';
import type { Schedule, ScheduleType } from '@/types/schedule';
import {
  WEEKDAY_LABELS,
  formToPayload,
  scheduleToForm,
  type ScheduleFormValues,
} from '@/lib/scheduleFormat';

const { TextArea } = Input;

export interface ScheduleEditorModalProps {
  open: boolean;
  editing: Schedule | null;
  /** 从智能体卡片/聊天页带入的预选应用 id。 */
  presetApplicationId?: number;
  onClose: () => void;
  onSaved: () => void;
}

const TIME_OPTIONS = Array.from({ length: 24 }, (_, h) => ({
  value: `${String(h).padStart(2, '0')}:00`,
  label: `${String(h).padStart(2, '0')}:00`,
}));

export function ScheduleEditorModal({
  open, editing, presetApplicationId, onClose, onSaved,
}: ScheduleEditorModalProps) {
  const [form] = Form.useForm<ScheduleFormValues>();
  const [saving, setSaving] = useState(false);
  const [apps, setApps] = useState<V2Application[]>([]);
  const [appsLoading, setAppsLoading] = useState(false);
  const [scheduleType, setScheduleType] = useState<ScheduleType>('daily');
  const [deliveryOn, setDeliveryOn] = useState(false);
  const [targets, setTargets] = useState<FeishuForwardTarget[]>([]);
  const [targetsLoading, setTargetsLoading] = useState(false);
  const [preview, setPreview] = useState<string[]>([]);
  const [previewing, setPreviewing] = useState(false);

  // 打开时初始化：编辑回填 or 新建默认值。
  useEffect(() => {
    if (!open) return;
    if (editing) {
      const v = scheduleToForm(editing);
      form.setFieldsValue(v);
      setScheduleType(v.schedule_type);
      setDeliveryOn(v.deliveries.length > 0);
      form.setFieldValue('deliveries', v.deliveries);
    } else {
      form.resetFields();
      form.setFieldsValue({
        schedule_type: 'daily',
        timezone: 'Asia/Shanghai',
        conversation_policy: 'new_each_run',
        overlap_policy: 'queue',
        misfire_policy: 'fire_once',
        deadline_policy: 'execute_anyway',
        execution_window_seconds: 0,
        trigger: { time: '09:00', days_of_week: [1, 2, 3, 4, 5], day_of_month: 1 },
        application_id: presetApplicationId,
        deliveries: [],
      });
      setScheduleType('daily');
      setDeliveryOn(false);
    }
    setPreview([]);
  }, [open, editing, presetApplicationId, form]);

  // 可调度应用：chat + 已启用 + 有运行时绑定（后端仍会做权威校验）。
  useEffect(() => {
    if (!open) return;
    setAppsLoading(true);
    fetchV2Applications('chat', { scope: 'mine', includeUnbound: false })
      .then((items) => setApps(items.filter((a) => a.enabled !== false && a.is_bound !== false)))
      .catch(() => setApps([]))
      .finally(() => setAppsLoading(false));
  }, [open]);

  // 飞书目标：开启投递时加载（用户 + 群聊）。
  useEffect(() => {
    if (!open || !deliveryOn) return;
    setTargetsLoading(true);
    Promise.all([
      fetchFeishuTargets('user').catch(() => []),
      fetchFeishuTargets('chat').catch(() => []),
    ])
      .then(([users, chats]) => setTargets([...users, ...chats]))
      .finally(() => setTargetsLoading(false));
  }, [open, deliveryOn]);

  const watchedAppId = Form.useWatch('application_id', form);
  const selectedApp = useMemo(
    () => apps.find((a) => a.id === watchedAppId),
    [apps, watchedAppId],
  );
  void selectedApp; // 展示预留：选中智能体的头像/名称随后续迭代上屏

  const refreshPreview = useCallback(async () => {
    try {
      setPreviewing(true);
      const values = await form.validateFields();
      const payload = formToPayload(values);
      const runs = await previewScheduleRuns(payload);
      setPreview(runs);
    } catch {
      // 校验失败时静默——表单自身会提示
    } finally {
      setPreviewing(false);
    }
  }, [form]);

  const handleOk = async () => {
    try {
      const values = await form.validateFields();
      const payload = formToPayload(values);
      if (!deliveryOn) payload.deliveries = undefined;
      setSaving(true);
      if (editing) {
        await updateSchedule(editing.id, payload);
        message.success('定时任务已更新');
      } else {
        await createSchedule(payload);
        message.success('定时任务已创建');
      }
      onSaved();
      onClose();
    } catch (e) {
      const msg = e instanceof Error ? e.message : '保存失败';
      message.error(msg);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      title={editing ? '编辑定时任务' : '新建定时任务'}
      open={open}
      onCancel={onClose}
      onOk={handleOk}
      confirmLoading={saving}
      okText={editing ? '保存' : '创建'}
      cancelText="取消"
      width={640}
      destroyOnClose
    >
      <Form form={form} layout="vertical" initialValues={{ trigger: { time: '09:00' } }}>
        <div className="schedule-editor-section">
          <div className="schedule-editor-section-title">基本信息</div>
          <Form.Item
            name="name"
            label="任务名称"
            rules={[{ required: true, message: '请输入任务名称' }, { max: 200, message: '名称过长' }]}
          >
            <Input placeholder="例如：每日销售日报" />
          </Form.Item>
          <Form.Item
            name="application_id"
            label="选择智能体"
            rules={[{ required: true, message: '请选择一个智能体' }]}
          >
            <Select
              loading={appsLoading}
              placeholder="选择要定时执行的智能体"
              showSearch
              optionFilterProp="label"
              options={apps.map((a) => ({
                value: a.id,
                label: a.name,
              }))}
            />
          </Form.Item>
          <Form.Item
            name="prompt"
            label="任务指令"
            rules={[{ required: true, message: '请输入每次执行要发送的指令' }]}
            extra="每次执行都会把这段指令作为新消息发送给智能体"
          >
            <TextArea rows={3} placeholder="例如：请总结今天的销售数据并发给我" />
          </Form.Item>
        </div>

        <div className="schedule-editor-section">
          <div className="schedule-editor-section-title">执行时间</div>
          <Form.Item name="schedule_type" label="重复方式" rules={[{ required: true }]}>
            <Radio.Group
              onChange={(e) => {
                setScheduleType(e.target.value);
                setPreview([]);
              }}
              options={[
                { value: 'once', label: '单次' },
                { value: 'daily', label: '每天' },
                { value: 'weekly', label: '每周' },
                { value: 'monthly', label: '每月' },
              ]}
              optionType="button"
              buttonStyle="solid"
            />
          </Form.Item>

          {scheduleType === 'once' ? (
            <Form.Item
              name="run_at_local"
              label="执行时间"
              rules={[{ required: true, message: '请选择执行时间' }]}
            >
              <DatePicker showTime={{ format: 'HH:mm' }} format="YYYY-MM-DD HH:mm" style={{ width: '100%' }} />
            </Form.Item>
          ) : (
            <>
              <Form.Item
                name={['trigger', 'time']}
                label="执行时刻"
                rules={[{ required: true, message: '请选择执行时刻' }]}
              >
                <Select options={TIME_OPTIONS} placeholder="09:00" />
              </Form.Item>
              {scheduleType === 'weekly' && (
                <Form.Item name={['trigger', 'days_of_week']} label="星期" rules={[{ required: true, message: '至少选择一天' }]}>
                  <Checkbox.Group options={WEEKDAY_LABELS.map((label, idx) => ({ value: idx, label }))} />
                </Form.Item>
              )}
              {scheduleType === 'monthly' && (
                <Form.Item
                  name={['trigger', 'day_of_month']}
                  label="日期"
                  rules={[{ required: true, message: '请选择日期' }]}
                  extra="遇短月自动顺延到当月最后一天"
                >
                  <InputNumber min={1} max={31} style={{ width: 120 }} />
                </Form.Item>
              )}
            </>
          )}

          <Form.Item name="timezone" label="时区" rules={[{ required: true }]}>
            <Select
              options={['Asia/Shanghai', 'UTC', 'Asia/Tokyo', 'Asia/Singapore', 'America/New_York', 'Europe/London'].map((tz) => ({ value: tz, label: tz }))}
            />
          </Form.Item>

          <button
            type="button"
            className="schedule-preview-btn"
            onClick={refreshPreview}
            disabled={previewing}
          >
            {previewing ? '计算中…' : '预览未来执行时间'}
          </button>
          {preview.length > 0 && (
            <ul className="schedule-preview-list" aria-live="polite">
              {preview.map((t) => (
                <li key={t}><time dateTime={t}>{new Date(t).toLocaleString()}</time></li>
              ))}
            </ul>
          )}
        </div>

        <div className="schedule-editor-section">
          <div className="schedule-editor-section-title">策略与会话</div>
          <Form.Item name="conversation_policy" label="会话方式" extra="建议每次新建会话，避免上下文无限累积">
            <Radio.Group
              options={[
                { value: 'new_each_run', label: '每次新建会话' },
                { value: 'reuse', label: '沿用首次会话' },
              ]}
            />
          </Form.Item>
          <Form.Item name="overlap_policy" label="上一轮未完成时">
            <Radio.Group
              options={[
                { value: 'queue', label: '排队等待' },
                { value: 'skip', label: '跳过本轮' },
              ]}
            />
          </Form.Item>
        </div>

        <div className="schedule-editor-section">
          <div className="schedule-editor-section-title">飞书投递（可选）</div>
          <Form.Item label="执行完成后发送飞书消息">
            <Switch checked={deliveryOn} onChange={setDeliveryOn} aria-label="开启飞书投递" />
          </Form.Item>
          {deliveryOn && (
            <>
              <Form.Item
                name={['deliveries', 0, 'target_id']}
                label="投递目标"
                rules={[{ required: true, message: '请选择投递目标' }]}
              >
                <Select
                  loading={targetsLoading}
                  showSearch
                  optionFilterProp="label"
                  placeholder="选择飞书用户或群聊"
                  options={targets.map((t) => ({
                    value: t.id,
                    label: `${t.target_type === 'chat' ? '[群聊] ' : ''}${t.name}`,
                  }))}
                  onSelect={(value) => {
                    const target = targets.find((t) => t.id === value);
                    if (target) {
                      form.setFieldsValue({
                        deliveries: [{
                          target_id: target.id,
                          target_name: target.name,
                          target_type: target.target_type,
                        }],
                      });
                    }
                  }}
                />
              </Form.Item>
              <Alert
                type="info"
                showIcon
                message="消息将以你的身份发送，内容为本次执行的最终回复。"
              />
            </>
          )}
        </div>
      </Form>
    </Modal>
  );
}
