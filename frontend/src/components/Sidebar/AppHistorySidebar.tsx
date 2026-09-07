import React, { useEffect, useMemo, useState } from 'react';
import { Menu, Empty, Popconfirm, message } from 'antd';
import { DeleteOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '@/stores/useAppStore';
import { useProjectStore } from '@/stores/useProjectStore';
import { api } from '@/services/api';
import type { WorkflowRun } from '@/types';

interface AppHistoryItem {
  key: string;
  kind: 'application' | 'workflow';
  title: string;
  subtitle: string;
  icon: string;
  updatedAt: string;
  projectId?: number;
  applicationSlug?: string;
  conversationId?: number;
  workflowRunId?: string;
  workflowStepId?: string;
  selectedStepId?: string;
}

const unwrap = <T,>(value: T[] | { results?: T[] }): T[] =>
  Array.isArray(value) ? value : value.results ?? [];

/**
 * App-center sidebar: category filter plus standalone app workspaces and the
 * application steps actually opened inside workflow runs.
 */
const AppHistorySidebar: React.FC = () => {
  const navigate = useNavigate();
  const { categories, selectedCategory, loadCategories, selectCategory } = useAppStore();
  const { projects, loadProjects, deleteProject } = useProjectStore();
  const [workflowRuns, setWorkflowRuns] = useState<WorkflowRun[]>([]);

  const historyItems = useMemo<AppHistoryItem[]>(() => {
    const standaloneItems: AppHistoryItem[] = projects
      .filter((project) => project.source === 'application' || !project.source)
      .filter((project) => project.application_kind !== 'chat'
        || Boolean(project.conversation_id))
      .map((project) => ({
        key: `application:${project.id}`,
        kind: 'application',
        title: project.title,
        subtitle: '独立应用',
        icon: '📂',
        updatedAt: project.updated_at,
        projectId: project.id,
        applicationSlug: project.application_slug || undefined,
        conversationId: project.conversation_id || undefined,
      }));
    const workflowItems: AppHistoryItem[] = workflowRuns.flatMap((run) =>
      run.step_runs
        .filter((stepRun) => Boolean(stepRun.last_opened_at))
        .map((stepRun) => ({
          key: `workflow:${stepRun.id}`,
          kind: 'workflow',
          title: stepRun.step.name || stepRun.step.application.application_name,
          subtitle: `工作流 · ${run.workflow_name}`,
          icon: stepRun.step.application.application_icon || '🔀',
          updatedAt: stepRun.last_opened_at || run.updated_at || run.created_at || '',
          workflowRunId: run.id,
          workflowStepId: stepRun.step.id,
          selectedStepId: run.selected_step_id,
        })),
    );
    return [...standaloneItems, ...workflowItems].sort(
      (left, right) => Date.parse(right.updatedAt) - Date.parse(left.updatedAt));
  }, [projects, workflowRuns]);

  useEffect(() => {
    loadCategories();
    loadProjects();
    api.get<WorkflowRun[] | { results?: WorkflowRun[] }>('/workflows/runs/')
      .then((response) => setWorkflowRuns(unwrap(response)))
      .catch(() => setWorkflowRuns([]));
  }, [loadCategories, loadProjects]);

  const openHistory = async (item: AppHistoryItem) => {
    try {
      if (item.kind === 'application' && item.projectId) {
        if (item.applicationSlug) {
          const params = new URLSearchParams({
            workspace: String(item.projectId),
          });
          if (item.conversationId) {
            params.set('conversation', String(item.conversationId));
          }
          navigate(
            `/apps/${encodeURIComponent(item.applicationSlug)}/run?${params.toString()}`,
          );
        } else {
          // Legacy records without an application association retain the old
          // project workspace fallback.
          navigate(`/workspace/${item.projectId}`);
        }
        return;
      }
      if (!item.workflowRunId || !item.workflowStepId) return;
      if (item.selectedStepId !== item.workflowStepId) {
        await api.post(`/workflows/runs/${item.workflowRunId}/select-step/`, {
          step_id: item.workflowStepId,
        });
      }
      navigate(`/workflow-runs/${item.workflowRunId}`);
    } catch {
      message.error('无法打开该执行记录');
    }
  };

  const menuItems = [
    { key: 'all', label: '全部应用' },
    ...categories.map((cat) => ({
      key: cat.slug,
      label: `${cat.icon ? cat.icon + ' ' : ''}${cat.name} (${cat.app_count ?? 0})`,
    })),
  ];

  return (
    <div className="tpl-sidebar">
      {/* Category filter */}
      <div className="tpl-sidebar-section">
        <div className="sidebar-title">应用分类</div>
        <Menu
          mode="inline"
          selectedKeys={[selectedCategory || 'all']}
          items={menuItems}
          onClick={({ key }) => selectCategory(key === 'all' ? null : key)}
        />
      </div>

      {/* History: recent workspaces */}
      <div className="tpl-sidebar-section">
        <div className="sidebar-title">历史记录</div>
        {historyItems.length === 0 ? (
          <div className="tpl-sidebar-empty">
            <Empty
              image={Empty.PRESENTED_IMAGE_SIMPLE}
              description={<span className="text-text-dim">还没有工作空间</span>}
            />
          </div>
        ) : (
          <div className="tpl-history-list">
            {historyItems.map((item) => (
              <div
                key={item.key}
                className="sidebar-item tpl-history-item"
                onClick={() => void openHistory(item)}
              >
                <span className="sidebar-item-icon">{item.icon}</span>
                <div className="tpl-history-body">
                  <div className="tpl-history-title">{item.title}</div>
                  <div className="tpl-history-sub">
                    {item.subtitle} · {new Date(item.updatedAt).toLocaleDateString('zh-CN')}
                  </div>
                </div>
                {item.kind === 'application' && item.projectId && (
                  <Popconfirm
                    title="删除该工作空间？"
                    description="其中的对话与文件将一并删除。"
                    okText="删除"
                    cancelText="取消"
                    okButtonProps={{ danger: true }}
                    onConfirm={(e) => {
                      e?.stopPropagation();
                      deleteProject(item.projectId as number);
                    }}
                    onCancel={(e) => e?.stopPropagation()}
                  >
                    <DeleteOutlined
                      className="tpl-history-delete"
                      onClick={(e) => e.stopPropagation()}
                    />
                  </Popconfirm>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
};

export default AppHistorySidebar;
