import { useNavigate } from 'react-router-dom';
import type { ApplicationSummary } from '@/services/runApi';
import { useIsMobile } from '@/shell/useIsMobile';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import CapabilityPickerDialog from './CapabilityPickerDialog';
import CapabilityPickerSheet from './CapabilityPickerSheet';
import './capability.css';

export default function CapabilityPicker({ open, onClose }: { open: boolean; onClose: () => void }) {
  const isMobile = useIsMobile();
  const navigate = useNavigate();
  const openApplication = useWorkspaceStore((state) => state.openApplication);
  const select = (item: ApplicationSummary) => {
    openApplication(item.id);
    onClose();
    navigate(item.kind === 'chat' ? `/chat/${item.slug}` : `/app/${item.slug}`);
  };
  const selectTask = (task: import('@/types/task').TaskSummary) => { onClose(); navigate(task.applicationSlug ? `/chat/${task.applicationSlug}?conversation=${task.id}` : `/?conversation=${task.id}`); };
  const props = { open, onClose, onSelect: select, onTaskSelect: selectTask };
  return isMobile ? <CapabilityPickerSheet {...props} /> : <CapabilityPickerDialog {...props} />;
}
