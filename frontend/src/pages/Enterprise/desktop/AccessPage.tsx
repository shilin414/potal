/**
 * AccessPage — desktop 权限管理（原样搬移自 EnterprisePage.tsx）。
 */
import React, { useCallback, useEffect, useState } from "react";
import {
  Button,
  Card,
  Drawer,
  Empty,
  Input,
  Radio,
  Select,
  Space,
  Switch,
  Table,
  TreeSelect,
  Typography,
  message,
} from "antd";
import type { ColumnsType } from "antd/es/table";
import { useLocation } from "react-router-dom";
import { useApplicationPage } from "@/hooks/useApplicationPage";
import type { V2Application } from "@/services/runApi";
import {
  enterpriseApi,
  type AccessMode,
  type AccessPolicy,
  type DirectoryDepartment,
  type DirectoryUser,
} from "../enterpriseApi";

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

export default function AccessPage({ kind }: { kind: "chat" | "fixed" }) {
  const location = useLocation();
  const initial = Number(new URLSearchParams(location.search).get("app") || 0);
  const [q, setQ] = useState("");
  const {
    items, loading, loadingMore, hasMore, loadMore,
  } = useApplicationPage({
    kind,
    scope: "manage",
    mode: "manage",
    includeUnbound: true,
    query: q,
    limit: 50,
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
      {hasMore && (
        <div style={{ textAlign: "center", marginTop: 16 }}>
          <Button loading={loadingMore} onClick={() => void loadMore()}>
            加载更多
          </Button>
        </div>
      )}
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
