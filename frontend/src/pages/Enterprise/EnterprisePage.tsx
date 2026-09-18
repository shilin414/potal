import React, { useCallback, useEffect, useMemo, useState } from "react";
import {
  Alert,
  Avatar,
  Button,
  Card,
  Col,
  Drawer,
  Empty,
  Form,
  Input,
  InputNumber,
  Layout,
  Menu,
  Modal,
  Popconfirm,
  Radio,
  Row,
  Select,
  Space,
  Statistic,
  Switch,
  Table,
  Tag,
  TimePicker,
  TreeSelect,
  Typography,
  message,
} from "antd";
import type { ColumnsType } from "antd/es/table";
import {
  ApartmentOutlined,
  AppstoreOutlined,
  AuditOutlined,
  CloudSyncOutlined,
  ControlOutlined,
  RobotOutlined,
  SafetyCertificateOutlined,
  TeamOutlined,
} from "@ant-design/icons";
import dayjs from "dayjs";
import { useLocation, useNavigate } from "react-router-dom";
import AgentEditorModal from "@/components/Agents/AgentEditorModal";
import { useApplicationPage } from "@/hooks/useApplicationPage";
import {
  createFixedApplication,
  deleteAgentApplication,
  fetchAgentRuntimes,
  updateAgentApplication,
  updateFixedApplication,
  type V2Application,
} from "@/services/runApi";
import {
  enterpriseApi,
  type AccessMode,
  type AccessPolicy,
  type AuditLog,
  type DirectoryDepartment,
  type DirectoryUser,
  type SyncConfig,
  type SyncRun,
} from "./enterpriseApi";
import "./EnterprisePage.css";

const { Sider, Content } = Layout;
const pagePath = (key: string) => `/enterprise/${key}`;
const menuItems = [
  { key: "overview", icon: <ControlOutlined />, label: "概览" },
  {
    type: "group" as const,
    label: "资源管理",
    children: [
      { key: "resources/agents", icon: <RobotOutlined />, label: "智能体管理" },
      { key: "resources/apps", icon: <AppstoreOutlined />, label: "应用管理" },
    ],
  },
  {
    type: "group" as const,
    label: "权限管理",
    children: [
      {
        key: "access/agents",
        icon: <SafetyCertificateOutlined />,
        label: "智能体授权",
      },
      {
        key: "access/apps",
        icon: <SafetyCertificateOutlined />,
        label: "应用授权",
      },
    ],
  },
  {
    type: "group" as const,
    label: "组织架构",
    children: [
      { key: "directory", icon: <TeamOutlined />, label: "部门与人员" },
      { key: "directory/sync", icon: <CloudSyncOutlined />, label: "同步管理" },
    ],
  },
  {
    type: "group" as const,
    label: "平台管理",
    children: [
      { key: "providers", icon: <ApartmentOutlined />, label: "Provider" },
      { key: "audit", icon: <AuditOutlined />, label: "审计日志" },
    ],
  },
];
const currentKey = (pathname: string) =>
  pathname.replace(/^\/enterprise\/?/, "") || "overview";
const fmt = (v?: string | null) => (v ? new Date(v).toLocaleString() : "—");

function ResourcePage({ kind }: { kind: "chat" | "fixed" }) {
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

function buildTree(deps: DirectoryDepartment[]) {
  const nodes = new Map<number, any>();
  deps.forEach((d) =>
    nodes.set(d.id, { title: d.name, value: d.id, key: d.id, children: [] }),
  );
  const roots: any[] = [];
  deps.forEach((d) => {
    const n = nodes.get(d.id);
    if (d.parent_id && nodes.has(d.parent_id))
      nodes.get(d.parent_id).children.push(n);
    else roots.push(n);
  });
  return roots;
}
function AccessPage({ kind }: { kind: "chat" | "fixed" }) {
  const location = useLocation();
  const initial = Number(new URLSearchParams(location.search).get("app") || 0);
  const [q, setQ] = useState("");
  const { items, loading } = useApplicationPage({
    kind,
    scope: "manage",
    mode: "manage",
    includeUnbound: true,
    query: q,
    limit: 100,
  });
  const [selected, setSelected] = useState<V2Application | null>(null);
  const [policy, setPolicy] = useState<AccessPolicy | null>(null);
  const [deps, setDeps] = useState<DirectoryDepartment[]>([]);
  const [users, setUsers] = useState<DirectoryUser[]>([]);
  const [saving, setSaving] = useState(false);
  const [includeChildren, setIncludeChildren] = useState(true);
  const open = useCallback(async (app: V2Application) => {
    setSelected(app);
    try {
      const [p, d, u] = await Promise.all([
        enterpriseApi.access(app.id),
        enterpriseApi.departments(),
        enterpriseApi.users({ limit: 100 }),
      ]);
      setPolicy(p);
      setDeps(d);
      setUsers(u.results);
    } catch {
      message.error("加载权限失败");
    }
  }, []);
  useEffect(() => {
    if (initial && items.length) {
      const app = items.find((x) => x.id === initial);
      if (app && !selected) void open(app);
    }
  }, [initial, items, open, selected]);
  const save = async () => {
    if (!selected || !policy) return;
    setSaving(true);
    try {
      const next = await enterpriseApi.updateAccess(selected.id, {
        access_mode: policy.access_mode,
        department_grants: policy.departments.map((d) => ({
          department_id: d.department_id,
          include_children: d.include_children,
        })),
        user_grants: policy.users.map((u) => u.directory_user_id),
      });
      setPolicy(next);
      message.success("访问权限已保存");
    } catch {
      message.error("保存失败，请检查部门和人员是否仍有效");
    } finally {
      setSaving(false);
    }
  };
  const setDepIds = (ids: number[]) => {
    if (!policy) return;
    const old = new Map(policy.departments.map((d) => [d.department_id, d]));
    setPolicy({
      ...policy,
      departments: ids.map(
        (id) =>
          old.get(id) || {
            department_id: id,
            name: deps.find((d) => d.id === id)?.name || "",
            include_children: includeChildren,
            covered_users: 0,
          },
      ),
    });
  };
  const searchUsers = async (value: string) => {
    try {
      const result = await enterpriseApi.users({ q: value, limit: 100 });
      setUsers(result.results);
    } catch {
      /* keep current options */
    }
  };
  const setUserIds = (ids: number[]) => {
    if (!policy) return;
    const old = new Map(policy.users.map((u) => [u.directory_user_id, u]));
    setPolicy({
      ...policy,
      users: ids.map(
        (id) =>
          old.get(id) || {
            directory_user_id: id,
            name: users.find((u) => u.id === id)?.name || "",
            avatar_url: users.find((u) => u.id === id)?.avatar_url || "",
            departments: (
              users.find((u) => u.id === id)?.departments || []
            ).map((d) => d.name),
          },
      ),
    });
  };
  const columns: ColumnsType<V2Application> = [
    { title: "资源", dataIndex: "name" },
    { title: "Slug", dataIndex: "slug" },
    {
      title: "操作",
      render: (_, app) => (
        <Button onClick={() => void open(app)}>设置权限</Button>
      ),
    },
  ];
  return (
    <section className="enterprise-section">
      <div className="enterprise-section__head">
        <div>
          <h2>{kind === "chat" ? "智能体授权" : "应用授权"}</h2>
          <p>资源配置与访问范围分离，部门和人员授权按 OR 计算</p>
        </div>
        <Input.Search
          placeholder="搜索资源"
          allowClear
          onChange={(e) => setQ(e.target.value)}
        />
      </div>
      <Table
        rowKey="id"
        loading={loading}
        dataSource={items}
        columns={columns}
        pagination={false}
      />
      <Drawer
        title={`访问权限 · ${selected?.name || ""}`}
        open={Boolean(selected)}
        onClose={() => {
          setSelected(null);
          setPolicy(null);
        }}
        width={620}
        extra={
          <Button type="primary" loading={saving} onClick={() => void save()}>
            保存
          </Button>
        }
      >
        {!policy ? (
          <Empty />
        ) : (
          <Space direction="vertical" size="large" style={{ width: "100%" }}>
            <div>
              <Typography.Title level={5}>访问范围</Typography.Title>
              <Radio.Group
                value={policy.access_mode}
                onChange={(e) =>
                  setPolicy({
                    ...policy,
                    access_mode: e.target.value as AccessMode,
                  })
                }
              >
                <Radio value="all">全体有效员工</Radio>
                <Radio value="assigned">指定范围</Radio>
                <Radio value="admin_only">仅管理员</Radio>
              </Radio.Group>
            </div>
            {policy.access_mode === "assigned" && (
              <>
                <div>
                  <Space>
                    <Typography.Title level={5} style={{ margin: 0 }}>
                      授权部门
                    </Typography.Title>
                    <Switch
                      checked={includeChildren}
                      onChange={setIncludeChildren}
                      checkedChildren="含子部门"
                      unCheckedChildren="仅本部门"
                    />
                  </Space>
                  <TreeSelect
                    treeData={buildTree(deps)}
                    treeCheckable
                    showCheckedStrategy={TreeSelect.SHOW_PARENT}
                    value={policy.departments.map((d) => d.department_id)}
                    onChange={setDepIds}
                    style={{ width: "100%", marginTop: 10 }}
                    placeholder="选择部门"
                  />
                  <Space
                    direction="vertical"
                    style={{ width: "100%", marginTop: 10 }}
                  >
                    {policy.departments.map((grant) => (
                      <Card size="small" key={grant.department_id}>
                        <Space
                          style={{
                            width: "100%",
                            justifyContent: "space-between",
                          }}
                        >
                          <span>
                            {grant.name ||
                              deps.find((d) => d.id === grant.department_id)
                                ?.name}{" "}
                            · 预计覆盖 {grant.covered_users || 0} 人
                          </span>
                          <Switch
                            checked={grant.include_children}
                            checkedChildren="含子部门"
                            unCheckedChildren="仅本部门"
                            onChange={(checked) =>
                              setPolicy({
                                ...policy,
                                departments: policy.departments.map((d) =>
                                  d.department_id === grant.department_id
                                    ? { ...d, include_children: checked }
                                    : d,
                                ),
                              })
                            }
                          />
                        </Space>
                      </Card>
                    ))}
                  </Space>
                </div>
                <div>
                  <Typography.Title level={5}>授权人员</Typography.Title>
                  <Select
                    mode="multiple"
                    showSearch
                    filterOption={false}
                    onSearch={(value) => void searchUsers(value)}
                    value={policy.users.map((u) => u.directory_user_id)}
                    onChange={setUserIds}
                    style={{ width: "100%" }}
                    options={users.map((u) => ({
                      value: u.id,
                      label: `${u.name} · ${u.departments.map((d) => d.name).join(" / ")}`,
                    }))}
                    placeholder="搜索并选择人员"
                  />
                </div>
              </>
            )}
          </Space>
        )}
      </Drawer>
    </section>
  );
}

function DirectoryPage() {
  const [deps, setDeps] = useState<DirectoryDepartment[]>([]);
  const [users, setUsers] = useState<DirectoryUser[]>([]);
  const [loading, setLoading] = useState(true);
  const [q, setQ] = useState("");
  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [d, u] = await Promise.all([
        enterpriseApi.departments({ include_inactive: true }),
        enterpriseApi.users({ q, include_inactive: true, limit: 100 }),
      ]);
      setDeps(d);
      setUsers(u.results);
    } finally {
      setLoading(false);
    }
  }, [q]);
  useEffect(() => {
    void load();
  }, [load]);
  return (
    <section className="enterprise-section">
      <div className="enterprise-section__head">
        <div>
          <h2>部门与人员</h2>
          <p>来自飞书 Directory 的只读企业目录快照</p>
        </div>
        <Input.Search allowClear placeholder="搜索人员" onSearch={setQ} />
      </div>
      <Row gutter={16}>
        <Col span={10}>
          <Card title={`部门 ${deps.length}`}>
            <Table
              size="small"
              rowKey="id"
              loading={loading}
              dataSource={deps}
              pagination={false}
              scroll={{ y: 560 }}
              columns={[
                { title: "部门", dataIndex: "name" },
                {
                  title: "状态",
                  render: (_, d) => (
                    <Tag color={d.is_active ? "green" : "default"}>
                      {d.is_active ? "有效" : "停用"}
                    </Tag>
                  ),
                },
              ]}
            />
          </Card>
        </Col>
        <Col span={14}>
          <Card title={`人员 ${users.length}`}>
            <Table
              size="small"
              rowKey="id"
              loading={loading}
              dataSource={users}
              pagination={false}
              scroll={{ y: 560 }}
              columns={[
                {
                  title: "人员",
                  render: (_, u) => (
                    <Space>
                      <Avatar src={u.avatar_url}>{u.name.slice(0, 1)}</Avatar>
                      {u.name}
                    </Space>
                  ),
                },
                {
                  title: "部门",
                  render: (_, u) =>
                    u.departments.map((d) => d.name).join(" / ") || "—",
                },
                {
                  title: "状态",
                  render: (_, u) => (
                    <Tag color={u.is_active ? "green" : "red"}>
                      {u.is_active ? "在职有效" : "无效/离职"}
                    </Tag>
                  ),
                },
                {
                  title: "已关联",
                  render: (_, u) =>
                    u.local_user_id ? (
                      <Tag color="blue">Potal 用户</Tag>
                    ) : (
                      "未登录"
                    ),
                },
              ]}
            />
          </Card>
        </Col>
      </Row>
    </section>
  );
}

function SyncPage() {
  const [cfg, setCfg] = useState<SyncConfig | null>(null);
  const [runs, setRuns] = useState<SyncRun[]>([]);
  const [form] = Form.useForm();
  const load = useCallback(async () => {
    const [c, r] = await Promise.all([
      enterpriseApi.syncConfig(),
      enterpriseApi.syncRuns(),
    ]);
    setCfg(c);
    setRuns(r);
    form.setFieldsValue({ ...c, daily_time: dayjs(c.daily_time, "HH:mm") });
  }, [form]);
  useEffect(() => {
    void load();
  }, [load]);
  const save = async () => {
    const v = await form.validateFields();
    const next = await enterpriseApi.updateSyncConfig({
      ...v,
      daily_time: v.daily_time.format("HH:mm"),
    });
    setCfg(next);
    message.success("同步设置已保存");
  };
  const trigger = async () => {
    await enterpriseApi.triggerSync();
    message.success("同步任务已进入队列");
    await load();
  };
  return (
    <section className="enterprise-section">
      <div className="enterprise-section__head">
        <div>
          <h2>同步管理</h2>
          <p>tenant_access_token + directory/v1，完整拉取后原子发布</p>
        </div>
        <Space>
          <Button onClick={() => void load()}>刷新</Button>
          <Button type="primary" onClick={() => void trigger()}>
            立即同步
          </Button>
        </Space>
      </div>
      {cfg && (
        <Alert
          type={runs[0]?.status === "failed" ? "error" : "info"}
          showIcon
          message={`最近成功：${fmt(cfg.last_success_at)} · 下次执行：${fmt(cfg.next_run_at)}`}
          description={
            runs[0]?.status === "failed" ? runs[0].error_message : undefined
          }
          style={{ marginBottom: 16 }}
        />
      )}
      <Card title="自动同步设置">
        <Form
          form={form}
          layout="inline"
          initialValues={{
            enabled: false,
            schedule_type: "interval",
            interval_minutes: 360,
            daily_time: dayjs("02:00", "HH:mm"),
            timezone: "Asia/Shanghai",
          }}
        >
          <Form.Item name="enabled" valuePropName="checked" label="启用">
            <Switch />
          </Form.Item>
          <Form.Item name="schedule_type" label="方式">
            <Select
              style={{ width: 120 }}
              options={[
                { value: "interval", label: "按间隔" },
                { value: "daily", label: "每天" },
              ]}
            />
          </Form.Item>
          <Form.Item noStyle shouldUpdate>
            {({ getFieldValue }) =>
              getFieldValue("schedule_type") === "daily" ? (
                <Form.Item name="daily_time" label="时间">
                  <TimePicker format="HH:mm" />
                </Form.Item>
              ) : (
                <Form.Item name="interval_minutes" label="间隔（分钟）">
                  <InputNumber min={15} max={10080} />
                </Form.Item>
              )
            }
          </Form.Item>
          <Form.Item name="timezone" label="时区">
            <Input style={{ width: 150 }} />
          </Form.Item>
          <Button type="primary" onClick={() => void save()}>
            保存
          </Button>
        </Form>
      </Card>
      <Card title="同步记录" style={{ marginTop: 16 }}>
        <Table
          rowKey="id"
          dataSource={runs}
          pagination={false}
          columns={[
            { title: "时间", dataIndex: "created_at", render: fmt },
            {
              title: "触发",
              dataIndex: "trigger_type",
              render: (v) => (v === "manual" ? "手动" : "自动"),
            },
            {
              title: "状态",
              dataIndex: "status",
              render: (v) => (
                <Tag
                  color={
                    v === "success" ? "green" : v === "failed" ? "red" : "blue"
                  }
                >
                  {v}
                </Tag>
              ),
            },
            { title: "部门", dataIndex: "departments_count" },
            {
              title: "员工（有效 / 总数）",
              render: (_, row) =>
                `${row.active_users_count} / ${row.users_count}`,
            },
            {
              title: "关系（有效 / 总数）",
              render: (_, row) =>
                `${row.active_memberships_count} / ${row.memberships_count}`,
            },
            { title: "错误", dataIndex: "error_message", ellipsis: true },
          ]}
        />
      </Card>
    </section>
  );
}
function AuditPage() {
  const [rows, setRows] = useState<AuditLog[]>([]);
  useEffect(() => {
    void enterpriseApi.audits().then(setRows);
  }, []);
  return (
    <section className="enterprise-section">
      <div className="enterprise-section__head">
        <div>
          <h2>审计日志</h2>
          <p>资源、授权与目录同步关键操作</p>
        </div>
      </div>
      <Table
        rowKey="id"
        dataSource={rows}
        columns={[
          { title: "时间", dataIndex: "created_at", render: fmt },
          { title: "动作", dataIndex: "action" },
          {
            title: "资源",
            render: (_, r) => `${r.resource_type}:${r.resource_id}`,
          },
          { title: "操作者", dataIndex: "user_id", render: (v) => v || "系统" },
          {
            title: "详情",
            dataIndex: "detail",
            render: (v) => (
              <Typography.Text code ellipsis style={{ maxWidth: 380 }}>
                {JSON.stringify(v)}
              </Typography.Text>
            ),
          },
        ]}
      />
    </section>
  );
}
function ProvidersPage() {
  const [rows, setRows] = useState<any[]>([]);
  useEffect(() => {
    void fetchAgentRuntimes().then(setRows);
  }, []);
  return (
    <section className="enterprise-section">
      <div className="enterprise-section__head">
        <div>
          <h2>Provider</h2>
          <p>当前已注册运行时能力</p>
        </div>
      </div>
      <Table
        rowKey="key"
        dataSource={rows}
        columns={[
          { title: "名称", dataIndex: "provider_name" },
          { title: "Provider Key", dataIndex: "provider_key" },
          { title: "Runtime", dataIndex: "runtime_type" },
          {
            title: "身份模式",
            dataIndex: "identity_modes",
            render: (v) => v?.join(", "),
          },
          {
            title: "执行模式",
            dataIndex: "execution_modes",
            render: (v) => v?.join(", "),
          },
        ]}
      />
    </section>
  );
}
function Overview() {
  const [stats, setStats] = useState({
    departments_total: 0,
    departments_active: 0,
    users_total: 0,
    users_active: 0,
    users_resigned: 0,
    oauth_users: 0,
    linked_directory_users: 0,
  });
  const [runs, setRuns] = useState<SyncRun[]>([]);
  useEffect(() => {
    void Promise.all([enterpriseApi.stats(), enterpriseApi.syncRuns(1)]).then(
      ([s, r]) => {
        setStats(s);
        setRuns(r);
      },
    );
  }, []);
  const match = stats.oauth_users
    ? Math.round((stats.linked_directory_users * 100) / stats.oauth_users)
    : 0;
  return (
    <section className="enterprise-section">
      <div className="enterprise-section__head">
        <div>
          <h2>企业控制台</h2>
          <p>统一管理资源、企业目录与访问权限</p>
        </div>
      </div>
      <Row gutter={[16, 16]}>
        <Col span={6}>
          <Card>
            <Statistic
              title="部门（有效 / 总数）"
              value={`${stats.departments_active} / ${stats.departments_total}`}
            />
          </Card>
        </Col>
        <Col span={6}>
          <Card>
            <Statistic
              title="员工（有效 / 总数）"
              value={`${stats.users_active} / ${stats.users_total}`}
            />
          </Card>
        </Col>
        <Col span={6}>
          <Card>
            <Statistic
              title="OAuth 关联"
              value={stats.linked_directory_users}
              suffix={`/ ${stats.oauth_users} · ${match}%`}
            />
          </Card>
        </Col>
        <Col span={6}>
          <Card>
            <Statistic title="最近同步" value={runs[0]?.status || "未执行"} />
          </Card>
        </Col>
      </Row>
      {stats.users_resigned > 0 && (
        <Alert
          type="info"
          showIcon
          message={`目录中有 ${stats.users_resigned} 名离职员工，ACL 已自动排除。`}
        />
      )}
    </section>
  );
}

export default function EnterprisePage() {
  const location = useLocation();
  const navigate = useNavigate();
  const key = currentKey(location.pathname);
  const content = useMemo(() => {
    if (key === "resources/agents") return <ResourcePage kind="chat" />;
    if (key === "resources/apps") return <ResourcePage kind="fixed" />;
    if (key.startsWith("access/agents")) return <AccessPage kind="chat" />;
    if (key.startsWith("access/apps")) return <AccessPage kind="fixed" />;
    if (key === "directory") return <DirectoryPage />;
    if (key === "directory/sync") return <SyncPage />;
    if (key === "providers") return <ProvidersPage />;
    if (key === "audit") return <AuditPage />;
    return <Overview />;
  }, [key]);
  return (
    <Layout className="enterprise-console">
      <Sider width={220} theme="light" className="enterprise-sider">
        <div className="enterprise-brand">企业控制台</div>
        <Menu
          mode="inline"
          selectedKeys={[key.split("?")[0]]}
          items={menuItems}
          onClick={({ key: k }) => navigate(pagePath(k))}
        />
      </Sider>
      <Content className="enterprise-content">{content}</Content>
    </Layout>
  );
}
