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
import {
  resolveApplication,
  type V2Application,
} from '@/services/runApi';
import { fetchFeishuTargets, type FeishuForwardTarget } from '@/services/shareApi';
import { createSchedule, previewScheduleRuns, updateSchedule } from '@/services/scheduleApi';
import { useApplicationPage } from '@/hooks/useApplicationPage';
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
  const [scheduleType, setScheduleType] = useState<ScheduleType>('daily');
  const [deliveryOn, setDeliveryOn] = useState(false);
  const [targets, setTargets] = useState<FeishuForwardTarget[]>([]);
  const [targetsLoading, setTargetsLoading] = useState(false);
  const [preview, setPreview] = useState<string[]>([]);
  const [previewing, setPreviewing] = useState(false);

  // 可调度智能体（二次复审 P1-2）：复用 useApplicationPage 走服务端分页 +
  // 服务端搜索（mode 'consume' 让后端过滤 enabled/bound），拥有 >50 个智能体
  // 也能通过 loadMore 全部选到；搜索词直发后端，浏览器不再只在前 N 条里
  // 本地过滤。编辑器关闭时 `enabled: false` 完全静默。
  const [appQuery, setAppQuery] = useState('');
  const {
    items: apps,
    loading: appsLoading,
    loadingMore: appsLoadingMore,
    hasMore: appsHasMore,
    loadMore: loadMoreApps,
  } = useApplicationPage({
    kind: 'chat',
    scope: 'mine',
    mode: 'consume',
    query: appQuery,
    limit: 50,
    enabled: open,
  });

  // Reopen starts from an empty search — the shells stay mounted, so the
  // previous session's term would otherwise filter the dropdown invisibly.
  useEffect(() => {
    if (!open) setAppQuery('');
  }, [open]);

  // 回填目标（二次复审 P1-2/P1-3）：编辑的已绑定智能体，或从智能体卡片/
  // 对话页带入的 presetApplicationId —— 两者都可能不在当前页（搜索词/翻页
  // 都够不到），按 ID resolve 一次注入 options，避免 Select 显示空白。
  const targetApplicationId = editing?.application_id ?? presetApplicationId ?? null;
  // 消费面必须走 resolveApplication（catalog visibility），绝不能走
  // fetchApplicationDetail —— 那是 staff-only 的 authoring 读法，/schedules
  // 普通用户也能进，旧逻辑让每个"目标不在第一页"的普通用户都先吃一条 403
  // 再退化成 “智能体 #id”。
  const [extraApp, setExtraApp] = useState<{ id: number; name: string } | null>(null);
  useEffect(() => {
    const id = targetApplicationId;
    if (!open || !id) {
      setExtraApp(null);
      return;
    }
    if (extraApp?.id === id) return;
    if (appsLoading) return; // 等 first page 到位再判断是否真的够不到
    if (apps.some((a) => a.id === id)) {
      // 目标已经在当前页：清掉上一次 editing 残留的 extraApp（§15 收口）。
      setExtraApp(null);
      return;
    }
    let stale = false;
    resolveApplication({ id })
      .then((resolved) => { if (!stale) setExtraApp({ id, name: resolved.name }); })
      .catch(() => { if (!stale) setExtraApp({ id, name: `智能体 #${id}` }); });
    return () => { stale = true; };
  }, [open, targetApplicationId, apps, appsLoading, extraApp]);

  // The editor reads `apps` for options — splice the resolved row in without
  // duplicating a value the current page already carries.
  const pickerApps = useMemo<V2Application[]>(() => (
    extraApp && !apps.some((a) => a.id === extraApp.id)
      ? [...apps, { id: extraApp.id, name: extraApp.name } as V2Application]
      : apps
  ), [apps, extraApp]);

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
    () => pickerApps.find((a) => a.id === watchedAppId),
    [pickerApps, watchedAppId],
  );
  void selectedApp; // 展示预留：选中智能体的头像/名称随后续迭代上屏

  return {
    form, saving,
    apps: pickerApps, appsLoading,
    appsHasMore, appsLoadingMore, loadMoreApps,
    appQuery, setAppQuery,
    scheduleType, setScheduleType, setPreview,
    deliveryOn, setDeliveryOn,
    targets, targetsLoading,
    preview, previewing, refreshPreview, handleOk,
  };
}

export type ScheduleEditorState = ReturnType<typeof useScheduleEditor>;
