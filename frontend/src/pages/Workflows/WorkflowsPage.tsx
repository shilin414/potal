import { useEffect, useState } from 'react';
import { Button, Card, Empty, Input, Modal, Spin, message } from 'antd';
import {
  EditOutlined, HistoryOutlined, PlayCircleOutlined, PlusOutlined,
} from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { api } from '@/services/api';
import type { Workflow, WorkflowRun } from '@/types';
import './Workflows.css';

const unwrap = <T,>(value: T[] | { results?: T[] }): T[] =>
  Array.isArray(value) ? value : value.results ?? [];

const WorkflowsPage = () => {
  const navigate = useNavigate();
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [runs, setRuns] = useState<WorkflowRun[]>([]);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState('');

  const load = async () => {
    setLoading(true);
    try {
      const [workflowResponse, runResponse] = await Promise.all([
        api.get<Workflow[] | { results?: Workflow[] }>('/workflows/'),
        api.get<WorkflowRun[] | { results?: WorkflowRun[] }>('/workflows/runs/'),
      ]);
      setWorkflows(unwrap(workflowResponse));
      setRuns(unwrap(runResponse));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { void load(); }, []);

  const create = async () => {
    if (!name.trim()) return;
    const workflow = await api.post<Workflow>('/workflows/', {
      name: name.trim(), description: '', icon: '🔀', is_public: false, steps: [],
    });
    setCreating(false);
    setName('');
    navigate(`/workflows/${workflow.id}/edit`);
  };

  const start = async (workflow: Workflow) => {
    try {
      const run = await api.post<{ id: string }>(`/workflows/${workflow.id}/start/`);
      navigate(`/workflow-runs/${run.id}`);
    } catch (error: any) {
      message.error(error?.response?.data?.detail || '工作流启动失败');
    }
  };

  return (
    <div className="workflows-page">
      <div className="workflows-heading">
        <div><h1>工作流</h1><p>把多个应用组合成一个人工执行的创作流程</p></div>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreating(true)}>
          新建工作流
        </Button>
      </div>
      {loading ? <div className="workflows-loading"><Spin size="large" /></div> : (
        <>
          {workflows.length === 0 ? <Empty description="还没有工作流" />
            : <div className="workflow-grid">{workflows.map((workflow) => (
              <Card key={workflow.id} className="workflow-card">
                <div className="workflow-card-icon">{workflow.icon || '🔀'}</div>
                <h3>{workflow.name}</h3>
                <p>{workflow.description || '未填写说明'}</p>
                <span>{workflow.step_count || 0} 个应用</span>
                <div className="workflow-card-actions">
                  <OutlinedButton onClick={() => navigate(`/workflows/${workflow.id}/edit`)} />
                  <Button type="primary" icon={<PlayCircleOutlined />}
                    disabled={!workflow.step_count} onClick={() => start(workflow)}>运行</Button>
                </div>
              </Card>
            ))}</div>}

          <section className="workflow-history">
            <div className="workflow-history-heading">
              <div><HistoryOutlined /><h2>执行历史</h2></div>
              <span>重新打开时仅恢复聊天应用的对话</span>
            </div>
            {runs.length === 0 ? (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无执行历史" />
            ) : (
              <div className="workflow-history-list">
                {runs.map((run) => {
                  const completed = run.step_runs.filter(
                    (stepRun) => stepRun.status === 'completed').length;
                  return (
                    <button
                      key={run.id}
                      className="workflow-history-item"
                      onClick={() => navigate(`/workflow-runs/${run.id}`)}
                    >
                      <span className="workflow-history-icon"><HistoryOutlined /></span>
                      <span className="workflow-history-copy">
                        <strong>{run.workflow_name}</strong>
                        <small>{new Date(run.updated_at || run.created_at || '').toLocaleString()}</small>
                      </span>
                      <span className="workflow-history-progress">
                        {completed}/{run.step_runs.length} 个应用
                      </span>
                      <span className={`workflow-history-status workflow-history-status--${run.status}`}>
                        {run.status === 'completed' ? '已完成'
                          : run.status === 'archived' ? '已归档' : '进行中'}
                      </span>
                    </button>
                  );
                })}
              </div>
            )}
          </section>
        </>
      )}
      <Modal title="新建工作流" open={creating} okText="创建" cancelText="取消"
        onOk={create} onCancel={() => setCreating(false)}>
        <Input autoFocus value={name} placeholder="例如：小红书内容生产"
          onChange={(event) => setName(event.target.value)} onPressEnter={create} />
      </Modal>
    </div>
  );
};

const OutlinedButton = ({ onClick }: { onClick: () => void }) => (
  <Button icon={<EditOutlined />} onClick={onClick}>编辑</Button>
);

export default WorkflowsPage;
