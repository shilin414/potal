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
import type { FeishuForwardTarget } from '@/services/shareApi';
import { createSchedule, previewScheduleRuns, updateSchedule } from '@/services/scheduleApi';
import { useApplicationPage } from '@/hooks/useApplicationPage';
import { useFeishuTargets } from '@/hooks/useFeishuTargets';
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
  const [preview, setPreview] = useState<string[]>([]);
  const [previewing, setPreviewing] = useState(false);

  // 会话代际（四次复审 P1-3）：open / editing?.id / presetApplicationId 任一
  // 变化 = 新编辑会话。旧会话晚到的保存响应不再操纵新会话的 UI —— 不
  // message.success、不 onSaved、不 onClose（曾能把正在编辑任务 B 的新编辑器
  // 直接关掉），saving 也立即归还新会话（不继承旧会话的 spinner）。
  // 注意边界：这里取消的只是「旧响应对新 UI 的操纵」，已发往服务器的
  // PATCH 本身无法撤销 —— 这是前端会话守卫的正确边界。
  const editorEpochRef = useRef(0);
  const saveSeqRef = useRef(0);
  const editingId = editing?.id ?? null;
  // 会话标识（五次复审 §38）：作为 useApplicationPage / useFeishuTargets 的
  // sessionKey —— 关闭 → null，编辑 A/B → 各自 id，新建 → 'new:preset'。
  const editorSessionKey = open
    ? (editingId ?? `new:${presetApplicationId ?? ''}`)
    : null;
  useEffect(() => {
    editorEpochRef.current += 1;
    saveSeqRef.current += 1;
    setSaving(false);
  }, [open, editingId, presetApplicationId]);

  // 可调度智能体（二次复审 P1-2）：复用 useApplicationPage 走服务端分页 +
  // 服务端搜索（mode 'consume' 让后端过滤 enabled/bound），拥有 >50 个智能体
  // 也能通过 loadMore 全部选到；搜索词直发后端，浏览器不再只在前 N 条里
  // 本地过滤。编辑器关闭时 `enabled: false` 完全静默。
  const [appQuery, setAppQuery] = useState('');
  // 投递目标搜索词（五次复审 P1-3）：飞书目标改为远程搜索 —— 不再在打开
  // 时预拉「前 20 个联系人 + 前 100 个群聊」然后本地过滤。
  const [targetQuery, setTargetQuery] = useState('');
  // 已选中的投递目标（五次复审 §24）：远程搜索下当前候选页可能不含它，
  // 必须把它注入 options，否则 Select 显示裸 id。
  const [selectedTarget, setSelectedTarget] = useState<FeishuForwardTarget | null>(null);

  // 会话切换的 render-time 重置（五次复审 §38）：新会话的那一帧就清空两个
  // 搜索词与已选目标 —— useApplicationPage / useFeishuTargets 的 sessionKey
  // 重置因此在同一次 commit 里拿到已清空的 query，首屏请求直接 q=''，
  // 不会先漏发一次旧词请求（旧的 !open effect 要等 passive effect + 300ms
  // 防抖，「关闭 50ms 后重开」仍会带旧词先请求一次）。
  const [prevEditorSession, setPrevEditorSession] = useState(editorSessionKey);
  if (prevEditorSession !== editorSessionKey) {
    setPrevEditorSession(editorSessionKey);
    setAppQuery('');
    setTargetQuery('');
    setSelectedTarget(null);
  }

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
    sessionKey: editorSessionKey,
  });

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
      // 回填的投递目标注入 options（五次复审 §24）：远程搜索下它不一定
      // 在当前候选页里，不注入 Select 就显示裸 id。
      setSelectedTarget(v.deliveries[0]
        ? {
          id: v.deliveries[0].target_id,
          name: v.deliveries[0].target_name,
          target_type: v.deliveries[0].target_type,
          avatar_url: '',
        }
        : null);
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
      setSelectedTarget(null);
    }
    setPreview([]);
  }, [open, editing, presetApplicationId, form]);

  // 飞书投递目标（五次复审 P1-3）：远程搜索，不再预拉全量后本地过滤。
  //   · user：空 query 不请求（provider 一页只有 20 条，空查询拿到的是
  //     「全公司任意前 20 人」）；输入姓名后服务端搜索；
  //   · chat：后端已按 page_token 翻页到 has_more=false，query 由后端
  //     按名称过滤，空 query = 全量群聊。
  // 两个 hook 独立请求、独立失败（ERROR ≠ EMPTY，五次复审 §28）——
  // 用户失败而群聊成功时仍展示群聊，只给「部分失败」警告 + 重试。
  const userTargets = useFeishuTargets({
    type: 'user',
    enabled: open && deliveryOn,
    query: targetQuery,
    sessionKey: editorSessionKey,
  });
  const chatTargets = useFeishuTargets({
    type: 'chat',
    enabled: open && deliveryOn,
    query: targetQuery,
    sessionKey: editorSessionKey,
  });

  // 候选 = 用户搜索结果 + 群聊（后端已过滤），已选目标始终保留在首位。
  const targets = useMemo<FeishuForwardTarget[]>(() => {
    const merged = [...userTargets.items, ...chatTargets.items];
    if (selectedTarget && !merged.some((t) => t.id === selectedTarget.id)) {
      merged.unshift(selectedTarget);
    }
    return merged;
  }, [userTargets.items, chatTargets.items, selectedTarget]);

  const targetsLoading = userTargets.loading || chatTargets.loading;
  const targetsFullyFailed = Boolean(userTargets.error && chatTargets.error);
  const targetsPartialFailed = !targetsFullyFailed
    && Boolean(userTargets.error || chatTargets.error);
  const targetsError = targetsFullyFailed
    ? (userTargets.error || chatTargets.error)
    : null;

  const refreshTargets = useCallback(() => {
    void userTargets.refresh();
    void chatTargets.refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [userTargets.refresh, chatTargets.refresh]);

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

  // 预览代际（四次复审 P2-5）：执行时间相关字段一变，旧预览立即作废并
  // 清空 —— 请求在途时改配置，晚到的旧预览不再误导（作废与 seq 守卫见
  // refreshPreview 与下方的触发字段 effect）。
  const previewSeqRef = useRef(0);

  const refreshPreview = useCallback(async () => {
    // 预览代际（四次复审 P2-5）：请求在途时用户还能继续改执行时间/星期/
    // 时区 —— 旧表单算出的预览晚到后不得覆盖新配置下的结果。
    const seq = ++previewSeqRef.current;
    try {
      setPreviewing(true);
      const payload = formToPayload(await readValues());
      const runs = await previewScheduleRuns(payload);
      if (seq !== previewSeqRef.current) return; // 表单已变，预览已作废
      setPreview(runs);
    } catch {
      // 校验失败时静默——表单自身会提示
    } finally {
      // 只有最新一次预览拥有 spinner 状态。
      if (seq === previewSeqRef.current) setPreviewing(false);
    }
  }, [readValues]);

  // 保存 single-flight（五次复审 P2-9）：handleOk 过去在 `await readValues()`
  // 之后才 setSaving(true) —— 当前校验都是同步规则所以撞不上，但未来引入
  // async validator 或组件层重复触发时，两个调用可以同时穿过 saving=false
  // 检查把 createSchedule 调两次。operation ref 在函数入口同步加锁；锁带
  // epoch 标记：会话换代后旧锁不阻塞新会话的保存，旧 operation 的 finally
  // 也只清理属于自己的锁（五次复审 §42：不要让旧 operation 把新会话的
  // synchronous lock 状态弄乱）。
  const saveInFlightRef = useRef<{ epoch: number } | null>(null);

  const handleOk = async () => {
    const saveEpoch = editorEpochRef.current;
    if (saveInFlightRef.current?.epoch === saveEpoch) return; // 同会话重复触发
    saveInFlightRef.current = { epoch: saveEpoch };
    const mySave = ++saveSeqRef.current;
    try {
      setSaving(true);
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
      if (editing) {
        await updateSchedule(editing.id, payload);
      } else {
        await createSchedule(payload);
      }
      // 保存请求结束时会话已换（关闭/重开/换了编辑对象，四次复审 P1-3）：
      // 旧响应不再 toast / onSaved / onClose —— 否则正在编辑任务 B 的
      // 新编辑器会被任务 A 的晚到响应直接关掉。
      if (editorEpochRef.current !== saveEpoch) return;
      message.success(editing ? '定时任务已更新' : '定时任务已创建');
      onSaved();
      onClose();
    } catch (e) {
      // 同理：旧会话的失败 toast 也不属于新会话。
      if (editorEpochRef.current === saveEpoch) {
        message.error(e instanceof Error ? e.message : '保存失败');
      }
    } finally {
      // 只有最新一次保存才复位 spinner；会话切换时 saveSeq 已被 bump 且
      // saving 已被会话 effect 置回 false，旧保存在此跳过即可。
      if (saveSeqRef.current === mySave) {
        setSaving(false);
      }
      // 只释放属于自己的锁：会话已换代时锁属于新 operation（或为空）。
      if (saveInFlightRef.current?.epoch === saveEpoch) {
        saveInFlightRef.current = null;
      }
    }
  };

  const watchedAppId = Form.useWatch('application_id', form);
  const selectedApp = useMemo(
    () => pickerApps.find((a) => a.id === watchedAppId),
    [pickerApps, watchedAppId],
  );
  void selectedApp; // 展示预留：选中智能体的头像/名称随后续迭代上屏

  // 回填告警只对「当前仍选中的原智能体」生效（四次复审 P2-4）：用户已经
  // 在 Select 里换成新 Agent 后，原 Agent 的 unavailable/transient 告警必须
  // 立即消失 —— 告警绑定的是当前字段值，而不是 resolve 时的目标。
  const resolutionApplies = watchedAppId === targetApplicationId;

  const watchedScheduleType = Form.useWatch('schedule_type', form);
  const watchedTriggerTime = Form.useWatch(['trigger', 'time'], form);
  const watchedDaysOfWeek = Form.useWatch(['trigger', 'days_of_week'], form);
  const watchedDayOfMonth = Form.useWatch(['trigger', 'day_of_month'], form);
  const watchedRunAt = Form.useWatch('run_at_local', form);
  const watchedTimezone = Form.useWatch('timezone', form);
  useEffect(() => {
    // 触发字段变化：在途预览立即作废 —— 清空结果并归还 spinner（被作废的
    // 预览自身已无权复位它）；其晚到的响应由 refreshPreview 的 seq 守卫丢弃。
    previewSeqRef.current += 1;
    setPreview([]);
    setPreviewing(false);
  }, [
    watchedScheduleType, watchedTriggerTime, watchedDaysOfWeek,
    watchedDayOfMonth, watchedRunAt, watchedTimezone,
  ]);

  return {
    form, saving,
    apps: pickerApps, appsLoading,
    appsHasMore, appsLoadingMore, loadMoreApps,
    // 列表失败 ≠ 空（§38–§39）：错误 + 刷新入口给到 Fields。
    appsError, refreshApps,
    // 回填状态（§40–§42）：404 = 原智能体不可用；5xx/network = 可重试。
    appResolution, targetApplicationId, retryResolveApp,
    // 换了智能体后旧告警立即隐藏（四次复审 P2-4）。
    resolutionApplies,
    appQuery, setAppQuery,
    scheduleType, setScheduleType, setPreview,
    deliveryOn, setDeliveryOn,
    // 飞书投递目标（五次复审 P1-3）：远程搜索 + 独立错误态 + 重试。
    targets, targetsLoading,
    targetsError, targetsPartialFailed, refreshTargets,
    targetQuery, setTargetQuery,
    setSelectedTarget,
    preview, previewing, refreshPreview, handleOk,
  };
}

export type ScheduleEditorState = ReturnType<typeof useScheduleEditor>;
