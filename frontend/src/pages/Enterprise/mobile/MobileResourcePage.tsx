/**
 * MobileResourcePage — 移动端资源管理（开发执行报告 §36–§38）。
 *
 * 智能体/应用共用一份列表（variant = kind）：名称 + slug + Provider/Runtime +
 * 启停状态 + •••。••• 打开 ActionSheet（编辑 / 权限 / 启停 / 删除）。
 * 编辑器复用桌面的 AgentEditorModal / 固定应用表单——列表换移动端，
 * 编辑表单本身不在本次重构范围（§68 不改后端、不动业务）。
 */
import React, { useCallback, useState } from 'react';
import {
  Alert,
  Button,
  Form,
  Input,
  Modal,
  Select,
  Skeleton,
  message,
} from 'antd';
import {
  DeleteOutlined,
  EditOutlined,
  SafetyCertificateOutlined,
} from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import AgentEditorModal from '@/components/Agents/AgentEditorModal';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import { useApplicationPage } from '@/hooks/useApplicationPage';
import {
  createFixedApplication,
  deleteAgentApplication,
  updateAgentApplication,
  updateFixedApplication,
  type V2Application,
} from '@/services/runApi';
import { useMobileHeader } from '@/shell/mobileHeader';
import { pagePath } from '../enterpriseNav';
import {
  MobileActionSheet,
  MobileEmptyState,
  MobileEntityRow,
  MobilePage,
  MobileSearchBar,
  type MobileAction,
} from '@/components/MobileConsole';
import '../EnterpriseMobile.css';

const KIND_LABELS: Record<string, string> = {
  page: '页面', form: '表单', dashboard: '看板', custom: '应用', task: '任务',
};

export default function MobileResourcePage({ kind }: { kind: 'chat' | 'fixed' }) {
  const navigate = useNavigate();
  const [q, setQ] = useState('');
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingId, setEditingId] = useState<number | null>(null);
  const [fixedOpen, setFixedOpen] = useState(false);
  const [fixedEditing, setFixedEditing] = useState<V2Application | null>(null);
  const [sheetFor, setSheetFor] = useState<V2Application | null>(null);
  const [form] = Form.useForm();
  const {
    items, loading, error, loadingMore, hasMore, loadMore, refresh, patchItem,
  } = useApplicationPage({
    kind,
    scope: 'manage',
    mode: 'manage',
    includeUnbound: true,
    query: q,
    limit: 50,
  });

  // useCallback + action:'create'：useMobileHeader 的 override 依赖 onAction
  // 引用稳定，否则 Provider 重渲染 → 页面重渲染 → 新函数 → 死循环；
  // action 必须显式声明——企业路由 handle 不带 action（§6/§37）。
  const openNew = useCallback(() => {
    if (kind === 'chat') {
      setEditingId(null);
      setEditorOpen(true);
    } else {
      setFixedEditing(null);
      form.resetFields();
      setFixedOpen(true);
    }
  }, [kind, form]);
  useMobileHeader({ action: 'create', onAction: openNew });

  const openEdit = (app: V2Application) => {
    if (kind === 'chat') {
      setEditingId(app.id);
      setEditorOpen(true);
    } else {
      setFixedEditing(app);
      form.setFieldsValue(app);
      setFixedOpen(true);
    }
  };

  const toggle = async (app: V2Application, enabled: boolean) => {
    try {
      await updateAgentApplication(app.id, { enabled });
      patchItem(app.id, { enabled });
      message.success(enabled ? '已启用' : '已停用');
    } catch {
      message.error('更新失败');
    }
  };

  const confirmRemove = (app: V2Application) => {
    Modal.confirm({
      title: `删除「${app.name}」？`,
      content: '删除后不可恢复。',
      okText: '删除',
      okButtonProps: { danger: true },
      cancelText: '取消',
      onOk: async () => {
        try {
          await deleteAgentApplication(app.id);
          await refresh();
          message.success('已删除');
        } catch {
          message.error('删除失败或资源已被引用');
        }
      },
    });
  };

  const saveFixed = async () => {
    try {
      const v = await form.validateFields();
      if (fixedEditing) {
        await updateFixedApplication(fixedEditing.id, {
          name: v.name, description: v.description, icon: v.icon, color: v.color,
        });
        message.success('应用已更新');
      } else {
        await createFixedApplication({ ...v, kind: v.kind, renderer_key: v.renderer_key });
        message.success('应用已注册，默认停用且仅管理员可见');
      }
      setFixedOpen(false);
      setFixedEditing(null);
      form.resetFields();
      await refresh();
    } catch {
      /* form or request */
    }
  };

  const actions: MobileAction[] = sheetFor ? [
    { key: 'edit', label: '编辑', icon: <EditOutlined />, onClick: () => openEdit(sheetFor) },
    {
      key: 'access',
      label: '设置访问权限',
      icon: <SafetyCertificateOutlined />,
      onClick: () => navigate(pagePath(
        `${kind === 'chat' ? 'access/agents' : 'access/apps'}?app=${sheetFor.id}`)),
    },
    {
      key: 'toggle',
      label: sheetFor.enabled !== false ? '停用' : '启用',
      onClick: () => void toggle(sheetFor, sheetFor.enabled === false),
    },
    {
      key: 'remove', label: '删除', icon: <DeleteOutlined />, danger: true,
      onClick: () => confirmRemove(sheetFor),
    },
  ] : [];

  const rowMeta = (app: V2Application) => (kind === 'chat'
    ? [app.provider_key, app.runtime_type].filter(Boolean).join(' · ') || '企业智能体'
    : [KIND_LABELS[app.kind] || app.kind, app.renderer_key].filter(Boolean).join(' · '));

  // 错误分类（二次复审 P2-10）：useApplicationPage 在 loadMore 失败时故意保留
  // 已加载行——fatal（首页失败、无数据）给整页错误态；partial（翻页/搜索失败）
  // 只给 Alert + 重试，已有列表绝不能消失。
  const fatalError = Boolean(error) && items.length === 0;
  const partialError = Boolean(error) && items.length > 0;
  // A failed loadMore keeps its cursor (retry = loadMore); a failed search /
  // first page clears it (retry = refresh).
  const retryPartial = () => (hasMore ? void loadMore() : void refresh());

  return (
    <MobilePage>
      <div className="mobile-console-page__sticky">
        <MobileSearchBar
          placeholder={kind === 'chat' ? '搜索智能体' : '搜索应用'}
          value={q}
          onChange={setQ}
        />
      </div>

      {fatalError ? (
        <MobileEmptyState
          title="加载资源失败"
          hint={error ?? undefined}
          action={<Button onClick={() => void refresh()}>重试</Button>}
        />
      ) : (
        <>
          {partialError && (
            <Alert
              type="error"
              showIcon
              message="加载失败"
              description={error}
              action={<Button size="small" onClick={retryPartial}>重试</Button>}
              style={{ marginTop: 12 }}
            />
          )}
          {loading && items.length === 0 ? (
            <div style={{ padding: '12px 0' }}>
              <Skeleton active avatar paragraph={{ rows: 1 }} />
              <Skeleton active avatar paragraph={{ rows: 1 }} />
              <Skeleton active avatar paragraph={{ rows: 1 }} />
            </div>
          ) : items.length === 0 ? (
            <MobileEmptyState
              title={kind === 'chat' ? '暂无智能体' : '暂无应用'}
              hint={kind === 'chat' ? '点击右上角新建智能体' : '点击右上角注册应用'}
            />
          ) : (
            <>
              <div className="mobile-console-section__rows" style={{ marginTop: 12 }}>
                {items.map((app) => (
                  <MobileEntityRow
                    key={app.id}
                    avatar={(
                      <AgentAvatar
                        application={app}
                        size={44}
                        shape={kind === 'chat' ? 'circle' : 'square'}
                        tint={app.color}
                      />
                    )}
                    title={app.name}
                    description={app.slug}
                    meta={rowMeta(app)}
                    badge={app.is_default_agent ? '默认' : undefined}
                    statusDot={app.enabled !== false ? 'on' : 'off'}
                    statusLabel={app.enabled !== false ? '启用' : '停用'}
                    onMore={() => setSheetFor(app)}
                    onClick={() => setSheetFor(app)}
                  />
                ))}
              </div>
              {hasMore && (
                <button
                  type="button"
                  className="mobile-console-more"
                  onClick={() => void loadMore()}
                >
                  {loadingMore ? '加载中…' : partialError ? '加载失败，点击重试' : '加载更多'}
                </button>
              )}
            </>
          )}
        </>
      )}

      <MobileActionSheet
        open={sheetFor !== null}
        title={sheetFor?.name}
        actions={actions}
        onClose={() => setSheetFor(null)}
      />

      {kind === 'chat' && (
        <AgentEditorModal
          agentId={editingId}
          mode="runtime"
          open={editorOpen}
          onClose={() => setEditorOpen(false)}
          onSaved={async () => {
            setEditorOpen(false);
            await refresh();
          }}
        />
      )}
      <Modal
        title={fixedEditing ? '编辑固定应用' : '注册固定应用'}
        open={fixedOpen}
        onCancel={() => {
          setFixedOpen(false);
          setFixedEditing(null);
        }}
        onOk={() => void saveFixed()}
      >
        <Form
          form={form}
          layout="vertical"
          style={{ marginTop: 16 }}
          initialValues={{ kind: 'page', icon: '🧩' }}
        >
          <Form.Item name="name" label="名称" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item
            name="slug"
            label="Slug"
            hidden={Boolean(fixedEditing)}
            rules={[{ required: true, pattern: /^[a-z0-9]+(?:-[a-z0-9]+)*$/ }]}
          >
            <Input />
          </Form.Item>
          <Form.Item name="kind" label="类型" hidden={Boolean(fixedEditing)}>
            <Select
              options={['page', 'form', 'dashboard', 'custom', 'task'].map(
                (value) => ({ value, label: value }),
              )}
            />
          </Form.Item>
          <Form.Item
            name="renderer_key"
            label="Renderer Key"
            hidden={Boolean(fixedEditing)}
            rules={[{ required: true }]}
          >
            <Input />
          </Form.Item>
          <Form.Item name="description" label="描述">
            <Input.TextArea />
          </Form.Item>
          <Form.Item name="icon" label="图标">
            <Input />
          </Form.Item>
          <Form.Item name="color" label="主题色">
            <Input placeholder="#2563eb" />
          </Form.Item>
        </Form>
      </Modal>
    </MobilePage>
  );
}
