import { Drawer } from 'antd';
import CapabilityPickerCore from './CapabilityPickerCore';
import type { ApplicationSummary } from '@/services/runApi';
import type { TaskSummary } from '@/types/task';

export default function CapabilityPickerSheet({ open, onClose, onSelect, onTaskSelect }: {
  open: boolean;
  onClose: () => void;
  onSelect: (item: ApplicationSummary) => void;
  onTaskSelect: (task: TaskSummary) => void;
}) {
  return (
    <Drawer open={open} onClose={onClose} placement="bottom" height="82dvh" title="选择能力" destroyOnClose>
      <CapabilityPickerCore onSelect={onSelect} onTaskSelect={onTaskSelect} />
    </Drawer>
  );
}

