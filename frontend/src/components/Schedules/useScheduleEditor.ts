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
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
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

/**
 * 回填目标的状态（三次复审 §40–§42）：resolveApplication 是消费面端点，
 * 404 = 原智能体已停用/解绑/Provider 停用/权限被移除（不可执行，需要换
 * 一个）；5xx / network = 临时故障（可重试），绝不伪装成「智能体 #id」。
 */
export type AppResolutionState = 'ready' | 'unavailable' | 'transient-error';

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
    // 列表失败 ≠ 没有智能体（三次复审 §38–§39）：错误必须可见 + 可重试，
    // 不能让 Select 看起来只是「空的」。
    error: appsError,
    refresh: refreshApps,
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
  const [appResolution, setAppResolution] = useState<AppResolutionState>('ready');
  // 仅作为 effect 的重跑信号（重试按钮），值本身不参与逻辑。
  const [resolveAttempt, setResolveAttempt] = useState(0);
  const resolvedForRef = useRef<number | null>(null);
  useEffect(() => {
    const id = targetApplicationId;
    if (!open || !id) {
      setExtraApp(null);
      setAppResolution('ready');
      resolvedForRef.current = null;
      return;
    }
    // 换了目标：先清掉上一个目标残留在屏的 unavailable/transient 状态。
    if (resolvedForRef.current !== id) {
      setExtraApp(null);
      setAppResolution('ready');
      resolvedForRef.current = id;
    }
    if (extraApp?.id === id) return;
    if (appsLoading) return; // 等 first page 到位再判断是否真的够不到
    if (apps.some((a) => a.id === id)) {
      // 目标已经在当前页：清掉上一次 editing 残留的 extraApp（§15 收口）。
      setExtraApp(null);
      setAppResolution('ready');
      return;
    }
    let stale = false;
    resolveApplication({ id })
      .then((resolved) => {
        if (stale) return;
        setExtraApp({ id, name: resolved.name });
        setAppResolution('ready');
      })
      .catch((err: unknown) => {
        if (stale) return;
        const status = (err as { response?: { status?: number } })?.response?.status;
        if (status === 404) {
          // 消费面 404 = 原智能体已不可执行（停用/解绑/Provider/权限）。
          // 仍注入 #id 占位让 Select 不空白，但保存必须先换一个。
          setExtraApp({ id, name: `智能体 #${id}` });
          setAppResolution('unavailable');
        } else {
          // 5xx / 网络 = 临时故障：不把故障伪装成「智能体 #id」，
          // 给独立错误 + 重试。
          setExtraApp(null);
          setAppResolution('transient-error');
        }
      });
    return () => { stale = true; };
  }, [open, targetApplicationId, apps, appsLoading, extraApp, resolveAttempt]);

  /** 重试回填 resolve（transient-error 专用，§42）。 */
  const retryResolveApp = useCallback(() => {
    setResolveAttempt((n) => n + 1);
  }, []);

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
      // 原智能体已不可执行（§41）：保存只会被后端 validation 以
      // "application is not schedulable" 拒绝 —— 在前端就拦下，要求换一个。
      if (
        appResolution === 'unavailable'
        && payload.application_id === targetApplicationId
      ) {
        message.error('原智能体当前不可用，请重新选择一个智能体');
        return;
      }
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
    // 列表失败 ≠ 空（§38–§39）：错误 + 刷新入口给到 Fields。
    appsError, refreshApps,
    // 回填状态（§40–§42）：404 = 原智能体不可用；5xx/network = 可重试。
    appResolution, targetApplicationId, retryResolveApp,
    appQuery, setAppQuery,
    scheduleType, setScheduleType, setPreview,
    deliveryOn, setDeliveryOn,
    targets, targetsLoading,
    preview, previewing, refreshPreview, handleOk,
  };
}

export type ScheduleEditorState = ReturnType<typeof useScheduleEditor>;
