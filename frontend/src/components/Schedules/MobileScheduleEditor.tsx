/**
 * MobileScheduleEditor — 新建/编辑定时任务的移动全屏版
 * (开发执行报告 §28)：MobileFullScreenDrawer + 与桌面 Modal 完全相同的
 * useScheduleEditor / ScheduleEditorFields，提交报文零分叉。
 */
import React from 'react';
import { MobileFullScreenDrawer } from '@/components/MobileConsole';
import type { Schedule } from '@/types/schedule';
import { useScheduleEditor } from './useScheduleEditor';
import { ScheduleEditorFields } from './ScheduleEditorFields';

export interface MobileScheduleEditorProps {
  open: boolean;
  editing: Schedule | null;
  presetApplicationId?: number;
  onClose: () => void;
  onSaved: () => void;
}

export function MobileScheduleEditor({
  open, editing, presetApplicationId, onClose, onSaved,
}: MobileScheduleEditorProps) {
  const state = useScheduleEditor({ open, editing, presetApplicationId, onSaved, onClose });

  return (
    <MobileFullScreenDrawer
      open={open}
      title={editing ? '编辑定时任务' : '新建定时任务'}
      actionText={editing ? '保存' : '创建'}
      actionLoading={state.saving}
      onAction={() => void state.handleOk()}
      onClose={onClose}
    >
      <ScheduleEditorFields state={state} />
    </MobileFullScreenDrawer>
  );
}
