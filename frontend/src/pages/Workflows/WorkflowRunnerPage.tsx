import { useCallback, useEffect, useMemo, useState } from 'react';
import { Button, Result, Spin, Tag, message } from 'antd';
import { ArrowLeftOutlined, CheckOutlined } from '@ant-design/icons';
import { useNavigate, useParams } from 'react-router-dom';
import ApplicationRuntime from '@/components/Applications/ApplicationRuntime';
import { api } from '@/services/api';
import type { WorkflowRun, WorkflowStepRun } from '@/types';
import './Workflows.css';

const WorkflowRunnerPage = () => {
  const { runId } = useParams<{ runId: string }>();
  const navigate = useNavigate();
  const [run, setRun] = useState<WorkflowRun | null>(null);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    if (!runId) return;
    setLoading(true);
    try {
      setRun(await api.get<WorkflowRun>(`/workflows/runs/${runId}/`));
    } finally {
      setLoading(false);
    }
  }, [runId]);

  useEffect(() => { void load(); }, [load]);

  const selected = useMemo(() => run?.step_runs.find(
    (item) => item.step.id === run.selected_step_id) ?? run?.step_runs[0], [run]);

  const selectStep = async (stepRun: WorkflowStepRun) => {
    if (!run) return;
    try {
      setRun(await api.post<WorkflowRun>(
        `/workflows/runs/${run.id}/select-step/`, { step_id: stepRun.step.id }));
    } catch (error: any) {
      message.error(error?.response?.data?.detail || '应用切换失败');
    }
  };

  const complete = async () => {
    if (!run || !selected) return;
    setRun(await api.post<WorkflowRun>(
      `/workflows/runs/${run.id}/complete-step/`, { step_id: selected.step.id }));
    message.success('已标记完成，可继续选择其他应用');
  };

  if (loading) return <div className="workflow-run-loading"><Spin size="large" /></div>;
  if (!run || !selected) return <Result status="404" title="工作流运行不存在" />;

  return (
    <div className="workflow-runner">
      <aside className="workflow-run-sidebar">
        <div className="workflow-run-brand">
          <Button type="text" icon={<ArrowLeftOutlined />} onClick={() => navigate('/workflows')} />
          <div><strong>{run.workflow_name}</strong><span>人工选择应用执行</span></div>
        </div>
        <div className="workflow-run-steps">
          {run.step_runs.map((stepRun, index) => (
            <button key={stepRun.id}
              className={`workflow-run-step ${selected.id === stepRun.id ? 'active' : ''}`}
              onClick={() => selectStep(stepRun)}>
              <span className="workflow-run-step-number">{index + 1}</span>
              <span className="workflow-run-step-icon">{stepRun.step.application.application_icon}</span>
              <span className="workflow-run-step-text">
                <strong>{stepRun.step.name || stepRun.step.application.application_name}</strong>
                <small>{stepRun.step.application.kind === 'chat' ? '聊天应用' : '任务应用'}</small>
              </span>
              {stepRun.status === 'completed' && <CheckOutlined className="workflow-step-done" />}
            </button>
          ))}
        </div>
      </aside>
      <section className="workflow-run-content">
        <header className="workflow-run-header">
          <div>
            <strong>{selected.step.name || selected.step.application.application_name}</strong>
            <Tag>{selected.step.application.kind}</Tag>
          </div>
          <Button icon={<CheckOutlined />} onClick={complete}>标记完成</Button>
        </header>
        <main className="workflow-run-application">
          <ApplicationRuntime key={selected.id}
            application={selected.step.application}
            projectId={run.project_id}
            workflowStepRunId={selected.id} />
        </main>
      </section>
    </div>
  );
};

export default WorkflowRunnerPage;
