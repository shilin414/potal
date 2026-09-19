import { DeleteOutlined, EditOutlined, EllipsisOutlined } from '@ant-design/icons';
import { Button, Dropdown, Modal } from 'antd';
import type { TaskSummary } from '@/types/task';

export default function TaskMenu({ task, onRename, onDelete }: {
  task: TaskSummary;
  onRename: (title: string) => Promise<void>;
  onDelete: () => Promise<void>;
}) {
  const rename = () => {
    const title = window.prompt('重命名任务', task.title || '未命名任务');
    if (title?.trim()) void onRename(title.trim());
  };
  const remove = () => Modal.confirm({
    title: '删除任务？',
    content: '删除后将同时移除该任务的消息与执行记录，此操作不可撤销。',
    okText: '删除任务',
    okButtonProps: { danger: true },
    cancelText: '取消',
    onOk: onDelete,
  });
  return (
    <Dropdown
      trigger={['click']}
      menu={{ items: [
        { key: 'rename', icon: <EditOutlined />, label: '重命名任务', onClick: rename },
        { key: 'delete', icon: <DeleteOutlined />, label: '删除任务', danger: true, onClick: remove },
      ] }}
    >
      <Button type="text" size="small" icon={<EllipsisOutlined />} aria-label="任务操作" onClick={(event) => event.stopPropagation()} />
    </Dropdown>
  );
}
