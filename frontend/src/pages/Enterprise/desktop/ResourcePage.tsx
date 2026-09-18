/**
 * ResourcePage — desktop 资源管理（原样搬移自 EnterprisePage.tsx）。
 */
import React, { useState } from "react";
import {
  Alert,
  Button,
  Form,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  message,
} from "antd";
import type { ColumnsType } from "antd/es/table";
import { useNavigate } from "react-router-dom";
import AgentEditorModal from "@/components/Agents/AgentEditorModal";
import { useApplicationPage } from "@/hooks/useApplicationPage";
import {
  createFixedApplication,
  deleteAgentApplication,
  updateAgentApplication,
  updateFixedApplication,
  type V2Application,
} from "@/services/runApi";
import { pagePath } from "../enterpriseNav";

export default function ResourcePage({ kind }: { kind: "chat" | "fixed" }) {
  const navigate = useNavigate();
  const [q, setQ] = useState("");
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingId, setEditingId] = useState<number | null>(null);
  const [fixedOpen, setFixedOpen] = useState(false);
  const [fixedEditing, setFixedEditing] = useState<V2Application | null>(null);
  const [form] = Form.useForm();
  const { items, loading, loadingMore, hasMore, loadMore, refresh, patchItem } =
    useApplicationPage({
      kind,
      scope: "manage",
      mode: "manage",
      includeUnbound: true,
      query: q,
      limit: 50,
    });
  const toggle = async (app: V2Application, enabled: boolean) => {
    try {
      await updateAgentApplication(app.id, { enabled });
      patchItem(app.id, { enabled });
      message.success(enabled ? "已启用" : "已停用");
    } catch {
      message.error("更新失败");
    }
  };
  const remove = async (id: number) => {
    try {
      await deleteAgentApplication(id);
      await refresh();
      message.success("已删除");
    } catch {
      message.error("删除失败或资源已被引用");
    }
  };
  const saveFixed = async () => {
    try {
      const v = await form.validateFields();
      if (fixedEditing) {
        await updateFixedApplication(fixedEditing.id, {
          name: v.name,
          description: v.description,
          icon: v.icon,
          color: v.color,
        });
        message.success("应用已更新");
      } else {
        await createFixedApplication({
          ...v,
          kind: v.kind,
          renderer_key: v.renderer_key,
        });
        message.success("应用已注册，默认停用且仅管理员可见");
      }
      setFixedOpen(false);
      setFixedEditing(null);
      form.resetFields();
      await refresh();
    } catch {
      /* form or request */
    }
  };
  const cols: ColumnsType<V2Application> = [
    {
      title: "名称",
      dataIndex: "name",
      render: (v, app) => (
        <Space>
          {app.icon || "🧩"}
          <strong>{v}</strong>
          {app.is_default_agent && <Tag color="gold">默认</Tag>}
        </Space>
      ),
    },
    { title: "Slug", dataIndex: "slug" },
    { title: "类型", dataIndex: "kind" },
    { title: "Renderer", dataIndex: "renderer_key" },
    ...(kind === "chat"
      ? ([
          { title: "Provider", dataIndex: "provider_key" },
          { title: "Runtime", dataIndex: "runtime_type" },
        ] as ColumnsType<V2Application>)
      : []),
    {
      title: "状态",
      dataIndex: "enabled",
      render: (v, app) => (
        <Switch checked={v !== false} onChange={(x) => void toggle(app, x)} />
      ),
    },
    {
      title: "操作",
      key: "actions",
      render: (_, app) => (
        <Space>
          {kind === "chat" ? (
            <Button
              size="small"
              onClick={() => {
                setEditingId(app.id);
                setEditorOpen(true);
              }}
            >
              编辑
            </Button>
          ) : (
            <Button
              size="small"
              onClick={() => {
                setFixedEditing(app);
                form.setFieldsValue(app);
                setFixedOpen(true);
              }}
            >
              编辑
            </Button>
          )}
          <Button
            size="small"
            onClick={() =>
              navigate(
                pagePath(
                  `${kind === "chat" ? "access/agents" : "access/apps"}?app=${app.id}`,
                ),
              )
            }
          >
            权限
          </Button>
          <Popconfirm title="确认删除？" onConfirm={() => void remove(app.id)}>
            <Button size="small" danger>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ];
  return (
    <section className="enterprise-section">
      <div className="enterprise-section__head">
        <div>
          <h2>{kind === "chat" ? "智能体管理" : "应用管理"}</h2>
          <p>
            {kind === "chat"
              ? "统一维护智能体、运行时和启停状态"
              : "注册随版本发布的固定应用 renderer"}
          </p>
        </div>
        <Space>
          <Input.Search
            placeholder="搜索资源"
            allowClear
            onSearch={setQ}
            onChange={(e) => setQ(e.target.value)}
          />
          <Button
            type="primary"
            onClick={() =>
              kind === "chat"
                ? (setEditingId(null), setEditorOpen(true))
                : (setFixedEditing(null),
                  form.resetFields(),
                  setFixedOpen(true))
            }
          >
            {kind === "chat" ? "新建智能体" : "注册应用"}
          </Button>
        </Space>
      </div>
      <Table
        rowKey="id"
        loading={loading}
        dataSource={items}
        columns={cols}
        pagination={false}
        scroll={{ x: 1000 }}
      />
      {hasMore && (
        <div className="enterprise-more">
          <Button loading={loadingMore} onClick={() => void loadMore()}>
            加载更多
          </Button>
        </div>
      )}
      {kind === "chat" && (
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
        title={fixedEditing ? "编辑固定应用" : "注册固定应用"}
        open={fixedOpen}
        onCancel={() => {
          setFixedOpen(false);
          setFixedEditing(null);
        }}
        onOk={() => void saveFixed()}
      >
        <Alert
          type="info"
          showIcon
          message="renderer_key 必须对应已随前端版本发布的渲染器。新应用默认停用、仅管理员可见。"
        />
        <Form
          form={form}
          layout="vertical"
          style={{ marginTop: 16 }}
          initialValues={{ kind: "page", icon: "🧩" }}
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
              options={["page", "form", "dashboard", "custom", "task"].map(
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
    </section>
  );
}
