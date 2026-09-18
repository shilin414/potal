/**
 * ScheduleEditorModal — 新建/编辑定时任务（Desktop Modal）。
 * 状态与字段拆到 useScheduleEditor / ScheduleEditorFields，与移动端
 * MobileScheduleEditor 共用同一份表单和提交报文（开发执行报告 §28）。
 */
import React from 'react';
import { Modal } from 'antd';
import type { Schedule } from '@/types/schedule';
import { useScheduleEditor } from './useScheduleEditor';
import { ScheduleEditorFields } from './ScheduleEditorFields';

export interface ScheduleEditorModalProps {
  open: boolean;
  editing: Schedule | null;
  /** 从智能体卡片/聊天页带入的预选应用 id。 */
  presetApplicationId?: number;
  onClose: () => void;
  onSaved: () => void;
}

export function ScheduleEditorModal({
  open, editing, presetApplicationId, onClose, onSaved,
}: ScheduleEditorModalProps) {
  const state = useScheduleEditor({ open, editing, presetApplicationId, onSaved, onClose });

  return (
    <Modal
      title={editing ? '编辑定时任务' : '新建定时任务'}
      open={open}
      onCancel={onClose}
      onOk={() => void state.handleOk()}
      confirmLoading={state.saving}
      okText={editing ? '保存' : '创建'}
      cancelText="取消"
      width={640}
      destroyOnClose
    >
      <ScheduleEditorFields state={state} />
    </Modal>
  );
}
