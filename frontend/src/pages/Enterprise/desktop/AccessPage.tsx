/**
 * AccessPage — desktop 权限管理（原样搬移自 EnterprisePage.tsx）。
 */
import React, { useCallback, useEffect, useRef, useState } from "react";
import {
  Alert,
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
import { fetchApplicationDetail, type V2Application } from "@/services/runApi";
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

/**
 * The permission drawer only ever needs an id + name — a deep-linked target
 * can be resolved through fetchApplicationDetail without waiting for its
 * page to arrive (二次复审 P1-1)。Enterprise 挂在 AdminRoute 下，staff
 * authoring detail 在这里是被允许的读法。
 */
interface AccessTarget {
  id: number;
  name: string;
}

export default function AccessPage({ kind }: { kind: "chat" | "fixed" }) {
  const location = useLocation();
  const initial = Number(new URLSearchParams(location.search).get("app") || 0);
  const [q, setQ] = useState("");
  const {
    items, loading, loadingMore, hasMore, loadMore, error, refresh,
  } = useApplicationPage({
    kind,
    scope: "manage",
    mode: "manage",
    includeUnbound: true,
    query: q,
    limit: 50,
  });
  const [selected, setSelected] = useState<AccessTarget | null>(null);
  const [policy, setPolicy] = useState<AccessPolicy | null>(null);
  const [deps, setDeps] = useState<DirectoryDepartment[]>([]);
  const [users, setUsers] = useState<DirectoryUser[]>([]);
  const [saving, setSaving] = useState(false);
  const [includeChildren, setIncludeChildren] = useState(true);
  const [deepLinkError, setDeepLinkError] = useState<string | null>(null);
  // One resolve attempt per deep-link id — a LATER loadMore may still surface
  // the row organically (the items.find branch picks it up), and a NEW
  // ?app= id always gets a fresh attempt. `deepLinkAttempt` only exists to
  // re-run the effect after an explicit retry (P2-6).
  const deepLinkTriedRef = useRef<number | null>(null);
  const [deepLinkAttempt, setDeepLinkAttempt] = useState(0);
  const open = useCallback(async (app: AccessTarget) => {
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
  // A changed ?app= id must clear the previous id's error banner (二次复审
  // P2-6).
  useEffect(() => {
    setDeepLinkError(null);
  }, [initial]);
  useEffect(() => {
    if (!initial || selected) return;
    const hit = items.find((x) => x.id === initial);
    if (hit) {
      setDeepLinkError(null);
      void open({ id: hit.id, name: hit.name });
      return;
    }
    if (loading || deepLinkTriedRef.current === initial) return;
    deepLinkTriedRef.current = initial;
    let stale = false;
    fetchApplicationDetail(initial)
      .then((detail) => {
        if (!stale) void open({ id: initial, name: detail.name });
      })
      .catch((err: any) => {
        if (stale) return;
        const status = err?.response?.status;
        setDeepLinkError(
          status === 403 || status === 404
            ? "资源不存在或无权限"
            : "加载目标资源失败",
        );
      });
    return () => {
      stale = true;
    };
  }, [initial, items, loading, selected, open, deepLinkAttempt]);
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
  // 与 MobileAccessPage 相同的数据语义（二次复审 P1-1/P2-11）：
  // fatal = 首页失败且没有任何数据；partial = 加载更多失败但表格保留。
  const fatalError = Boolean(error) && items.length === 0;
  const partialError = Boolean(error) && items.length > 0;
  // A failed loadMore keeps its cursor (retry = loadMore); a failed search /
  // first page clears it (retry = refresh).
  const retryPartial = () => (hasMore ? void loadMore() : void refresh());
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
      {fatalError ? (
        <Alert
          type="error"
          showIcon
          message="加载资源失败"
          description={error}
          action={
            <Button size="small" onClick={() => void refresh()}>
              重试
            </Button>
          }
        />
      ) : (
        <>
          {deepLinkError && (
            <Alert
              type="warning"
              showIcon
              message={deepLinkError}
              style={{ marginBottom: 16 }}
              action={
                deepLinkError === "加载目标资源失败" ? (
                  <Button
                    size="small"
                    onClick={() => {
                      deepLinkTriedRef.current = null;
                      setDeepLinkAttempt((n) => n + 1);
                    }}
                  >
                    重试
                  </Button>
                ) : undefined
              }
            />
          )}
          <Table
            rowKey="id"
            loading={loading}
            dataSource={items}
            columns={columns}
            pagination={false}
            locale={{ emptyText: <Empty description="暂无可配置的资源" /> }}
          />
          {/* One CTA, never two: a failed loadMore REPLACES 加载更多 (P2-3). */}
          {partialError ? (
            <div style={{ textAlign: "center", marginTop: 16 }}>
              <Button danger onClick={retryPartial}>
                加载失败，点击重试
              </Button>
            </div>
          ) : hasMore ? (
            <div style={{ textAlign: "center", marginTop: 16 }}>
              <Button loading={loadingMore} onClick={() => void loadMore()}>
                加载更多
              </Button>
            </div>
          ) : null}
        </>
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
