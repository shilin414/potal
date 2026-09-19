import { useState } from 'react';
import { DownOutlined } from '@ant-design/icons';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import type { ApplicationSummary, ComposerApplication } from '@/services/runApi';
import CapabilityPicker from '../capability/CapabilityPicker';
import './workbench-home.css';

export default function WorkbenchHome({ current, recommended, recent, onOpen }: {
  current: ComposerApplication | null;
  recommended: ApplicationSummary[];
  recent: ApplicationSummary[];
  onOpen: (item: ApplicationSummary) => void;
}) {
  const [pickerOpen, setPickerOpen] = useState(false);
  return (
    <div className="workbench-home">
      <div className="workbench-home__hero">
        {current && <AgentAvatar application={current} size={58} tint={current.color} />}
        <h1>今天想完成什么？</h1>
        <button type="button" className="workbench-home__current" onClick={() => setPickerOpen(true)}>
          {current?.name || '选择能力'} <DownOutlined />
        </button>
        <p>选择智能体或应用，然后描述你的任务。</p>
      </div>
      {recent.length > 0 && <section><h2>最近使用</h2><div className="workbench-home__rows">{recent.slice(0, 8).map((item) => (
        <button key={item.id} type="button" onClick={() => onOpen(item)}><AgentAvatar application={item} size={32} tint={item.color} /><span>{item.name}</span></button>
      ))}</div></section>}
      {recommended.length > 0 && <section><h2>推荐能力</h2><div className="workbench-home__rows">{recommended.slice(0, 6).map((item) => (
        <button key={item.id} type="button" onClick={() => onOpen(item)}><AgentAvatar application={item} size={32} tint={item.color} /><span>{item.name}</span></button>
      ))}</div></section>}
      <CapabilityPicker open={pickerOpen} onClose={() => setPickerOpen(false)} />
    </div>
  );
}
