/**
 * ScheduleEditorFields — the editor form itself, shared by the desktop Modal
 * and the mobile full-screen drawer (开发执行报告 §28). One field list =
 * one payload contract; the shells only differ in chrome.
 */
import React from 'react';
import {
  Alert,
  Button,
  Checkbox,
  DatePicker,
  Form,
  Input,
  InputNumber,
  Radio,
  Select,
  Switch,
} from 'antd';
import { WEEKDAY_LABELS } from '@/lib/scheduleFormat';
import type { ScheduleType } from '@/types/schedule';
import type { ScheduleEditorState } from './useScheduleEditor';

const { TextArea } = Input;

const TIME_OPTIONS = Array.from({ length: 24 }, (_, h) => ({
  value: `${String(h).padStart(2, '0')}:00`,
  label: `${String(h).padStart(2, '0')}:00`,
}));

export function ScheduleEditorFields({ state }: { state: ScheduleEditorState }) {
  const {
    form, apps, appsLoading, appsHasMore, appsLoadingMore, loadMoreApps,
    appsError, refreshApps,
    appResolution, retryResolveApp,
    setAppQuery, scheduleType, setScheduleType,
    setPreview, deliveryOn, setDeliveryOn, targets, targetsLoading,
    preview, previewing, refreshPreview,
  } = state;

  return (
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
          {/* 服务端搜索 + 分页（二次复审 P1-2）：filterOption=false 让搜索词
              直发后端（覆盖全部智能体，而不只是已加载页）；下拉滚到底再拉
              下一页，>50 个可调度智能体也不会被截断。notFoundContent 区分
              loading / empty / error（三次复审 §39）。 */}
          <Select
            loading={appsLoading}
            placeholder="选择要定时执行的智能体"
            showSearch
            filterOption={false}
            onSearch={setAppQuery}
            onPopupScroll={(e) => {
              const { scrollTop, scrollHeight, clientHeight } = e.currentTarget;
              if (appsHasMore
                && !appsLoadingMore
                && scrollHeight - scrollTop - clientHeight < 24) {
                void loadMoreApps();
              }
            }}
            notFoundContent={
              appsError
                ? '加载智能体失败'
                : appsLoading
                  ? '搜索中…'
                  : '没有匹配的智能体'
            }
            options={apps.map((a) => ({
              value: a.id,
              label: a.name,
            }))}
          />
        </Form.Item>
        {/* 列表请求失败 ≠ 没有智能体（三次复审 §38–§39）：错误可见 + 重试。 */}
        {appsError && (
          <Alert
            type="error"
            showIcon
            message="加载智能体失败"
            description={appsError}
            action={<Button size="small" onClick={() => void refreshApps()}>重试</Button>}
            style={{ marginBottom: 16 }}
          />
        )}
        {/* 回填状态（§40–§42）：404 = 原智能体不可执行，必须换一个；
            5xx / 网络 = 临时故障，给重试而不是伪装成「智能体 #id」。 */}
        {appResolution === 'unavailable' && (
          <Alert
            type="warning"
            showIcon
            message="原智能体当前不可用，请选择新的智能体"
            style={{ marginBottom: 16 }}
          />
        )}
        {appResolution === 'transient-error' && (
          <Alert
            type="error"
            showIcon
            message="无法加载智能体信息"
            action={<Button size="small" onClick={retryResolveApp}>重试</Button>}
            style={{ marginBottom: 16 }}
          />
        )}
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
              setScheduleType(e.target.value as ScheduleType);
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
  );
}
