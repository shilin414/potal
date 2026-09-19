import { Modal } from 'antd';
import CapabilityPickerCore from './CapabilityPickerCore';
import type { ApplicationSummary } from '@/services/runApi';
import type { TaskSummary } from '@/types/task';

export default function CapabilityPickerDialog({ open, onClose, onSelect, onTaskSelect }: {
  open: boolean;
  onClose: () => void;
  onSelect: (item: ApplicationSummary) => void;
  onTaskSelect: (task: TaskSummary) => void;
}) {
  return (
    <Modal open={open} onCancel={onClose} footer={null} title="选择能力" width={620} destroyOnClose>
      <CapabilityPickerCore onSelect={onSelect} onTaskSelect={onTaskSelect} />
    </Modal>
  );
}

