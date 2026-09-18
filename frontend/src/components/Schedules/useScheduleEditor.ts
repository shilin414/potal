/**
 * useScheduleEditor — the editor's entire state machine, shared verbatim by
 * the desktop Modal and the mobile full-screen drawer (开发执行报告 §28).
 *
 * Extracted so the two presentations can never diverge on payload semantics
 * — in particular `readValues`, which MUST read `getFieldsValue(true)` (the
 * whole store) because `validateFields()` only returns REGISTERED Form.Item
 * paths and silently drops `deliveries[0].target_type/target_name` (see
 * scheduleEditorPayload.test).
 */
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Form, message } from 'antd';
import { fetchApplicationPage, type V2Application } from '@/services/runApi';
import { fetchFeishuTargets, type FeishuForwardTarget } from '@/services/shareApi';
import { createSchedule, previewScheduleRuns, updateSchedule } from '@/services/scheduleApi';
import type { Schedule, ScheduleType } from '@/types/schedule';
import {
  formToPayload,
  scheduleToForm,
  type ScheduleFormValues,
} from '@/lib/scheduleFormat';

export interface UseScheduleEditorOptions {
  open: boolean;
  editing: Schedule | null;
  /** 从智能体卡片/聊天页带入的预选应用 id。 */
  presetApplicationId?: number;
  onSaved: () => void;
  onClose: () => void;
}

export function useScheduleEditor({
  open, editing, presetApplicationId, onSaved, onClose,
}: UseScheduleEditorOptions) {
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
  // Reads the PAGED endpoint (执行报告 §16.2) — a schedule form must not
  // download the whole catalog to render a dropdown.
  useEffect(() => {
    if (!open) return;
    setAppsLoading(true);
    fetchApplicationPage({ kind: 'chat', scope: 'mine', limit: 100 })
      .then((page) => setApps(page.items.filter(
        (a) => a.enabled !== false && a.is_bound !== false)))
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

  /**
   * 校验并读回表单值。
   *
   * 必须用 `getFieldsValue(true)`（全量 store）而不是 `validateFields()` 的返回值：
   * 后者只包含**已注册 Form.Item 的路径**（rc-field-form 用注册路径重建对象），
   * 因此通过 `setFieldsValue` 写入、但没有对应 Form.Item 的字段会被静默丢弃 ——
   * 飞书投递的 `deliveries[0].target_name/target_type`（只有 target_id 注册了
   * Form.Item）以及 misfire_policy/deadline_policy/execution_window_seconds
   * 都会消失，后端随即以 `delivery target_type must be user or chat` 拒绝创建。
   */
  const readValues = useCallback(async (): Promise<ScheduleFormValues> => {
    await form.validateFields();
    return form.getFieldsValue(true) as ScheduleFormValues;
  }, [form]);

  const refreshPreview = useCallback(async () => {
    try {
      setPreviewing(true);
      const payload = formToPayload(await readValues());
      const runs = await previewScheduleRuns(payload);
      setPreview(runs);
    } catch {
      // 校验失败时静默——表单自身会提示
    } finally {
      setPreviewing(false);
    }
  }, [readValues]);

  const handleOk = async () => {
    try {
      const payload = formToPayload(await readValues());
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

  const watchedAppId = Form.useWatch('application_id', form);
  const selectedApp = useMemo(
    () => apps.find((a) => a.id === watchedAppId),
    [apps, watchedAppId],
  );
  void selectedApp; // 展示预留：选中智能体的头像/名称随后续迭代上屏

  return {
    form, saving, apps, appsLoading,
    scheduleType, setScheduleType, setPreview,
    deliveryOn, setDeliveryOn,
    targets, targetsLoading,
    preview, previewing, refreshPreview, handleOk,
  };
}

export type ScheduleEditorState = ReturnType<typeof useScheduleEditor>;
