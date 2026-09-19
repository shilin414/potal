import { useState } from 'react';
import { DownOutlined } from '@ant-design/icons';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import CapabilityPicker from '@/workbench/capability/CapabilityPicker';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import './MobileAgentSwitcher.css';

export default function MobileAgentSwitcher({ activeApplicationId }: { activeApplicationId?: number | null }) {
  const [open, setOpen] = useState(false);
  const storeId = useWorkspaceStore((state) => state.activeApplicationId);
  const resolvedApplication = useApplicationEntityStore((state) => state.byId[activeApplicationId ?? storeId ?? -1]);
  const defaultApplication = useWorkspaceBootstrapStore((state) => state.defaultApplication);
  const application = resolvedApplication || defaultApplication;
  return (
    <>
      <button type="button" className="mobile-agent-switcher" onClick={() => setOpen(true)} aria-label="选择能力">
        {application && <AgentAvatar application={application} size={28} tint={application.color} />}
        <span>{application?.name || '选择能力'}</span><DownOutlined />
      </button>
      <CapabilityPicker open={open} onClose={() => setOpen(false)} />
    </>
  );
}
