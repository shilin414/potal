import { CloseOutlined } from '@ant-design/icons';
import { useWorkbenchUiStore } from '@/stores/useWorkbenchUiStore';

export default function InspectorHost() {
  const open = useWorkbenchUiStore((state) => state.inspectorOpen);
  const tab = useWorkbenchUiStore((state) => state.inspectorTab);
  const close = useWorkbenchUiStore((state) => state.closeInspector);
  if (!open) return null;
  const labels = { task: '任务', run: '执行', artifacts: '成果', context: '上下文', app: '应用' };
  return (
    <aside className="workbench-inspector" aria-label="工作台详情">
      <header><strong>{labels[tab]}</strong><button type="button" onClick={close} aria-label="关闭详情"><CloseOutlined /></button></header>
      <div className="workbench-inspector__body">
        {tab === 'task' && <p>任务信息与最近活动会显示在这里。</p>}
        {tab === 'run' && <p>当前执行状态、耗时与事件时间线。</p>}
        {tab === 'artifacts' && <p>当前任务产生的文件、图片与其他成果。</p>}
        {tab === 'context' && <p>当前任务使用的技能、附件与上下文。</p>}
        {tab === 'app' && <p>应用说明与操作面板。</p>}
      </div>
    </aside>
  );
}
