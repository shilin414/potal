/**
 * AccessPage — desktop 权限管理（原样搬移自 EnterprisePage.tsx）。
 *
 * 权限加载/保存状态机由 useAccessPolicyEditor 承载（三次复审 P0）：目标
 * 切换竞态、保存 target guard、policy.application_id 不变量都在共享层。
 */
import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Alert,
  Button,
  Card,
  Drawer,
  Empty,
  Input,
  Radio,
  Select,
  Skeleton,
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
  type AccessMode,
  type DirectoryDepartment,
} from "../enterpriseApi";
import { useAccessPolicyEditor } from "../hooks/useAccessPolicyEditor";
import { useDirectoryUsers } from "../hooks/useDirectoryUsers";

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
  const [includeChildren, setIncludeChildren] = useState(true);
  const [deepLinkError, setDeepLinkError] = useState<string | null>(null);

  // Desktop/Mobile 同一数据语义（四次复审 P2-2）：授权人员 Select 也走
  // useDirectoryUsers 的服务端搜索 + cursor 分页（下拉滚动加载），与
  // MobileUserPicker 完全一致 —— 企业 10000 人时不再被「前 100 条」截断。
  const [userQuery, setUserQuery] = useState("");
  const directoryUsers = useDirectoryUsers({
    query: userQuery,
    enabled: Boolean(selected),
    // ACL 只提供在职员工（二次复审 D1）：离职人员不再出现在候选里。
    includeInactive: false,
  });

  const {
    policy, departments: deps,
    loading: policyLoading, loadError: policyLoadError,
    saving, ready, reload, save,
    setAccessMode, setDepartmentIds, patchDepartmentGrant, setUserGrants,
  } = useAccessPolicyEditor({
    applicationId: selected?.id ?? null,
    enabled: Boolean(selected),
    onSaved: () => { message.success("访问权限已保存"); },
  });
  // One resolve attempt per deep-link id — a LATER loadMore may still surface
  // the row organically (the items.find branch picks it up), and a NEW
  // ?app= id always gets a fresh attempt. `deepLinkAttempt` only exists to
  // re-run the effect after an explicit retry (P2-6).
  const deepLinkTriedRef = useRef<number | null>(null);
  const [deepLinkAttempt, setDeepLinkAttempt] = useState(0);

  // Opening a drawer selects its target; the shared hook loads the policy
  // (guarded) and useDirectoryUsers loads the first candidate page.
  const open = useCallback((app: AccessTarget) => {
    setSelected(app);
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
      open({ id: hit.id, name: hit.name });
      return;
    }
    if (loading || deepLinkTriedRef.current === initial) return;
    deepLinkTriedRef.current = initial;
    let stale = false;
    fetchApplicationDetail(initial)
      .then((detail) => {
        if (!stale) open({ id: initial, name: detail.name });
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

  // 已授权人员永远有名字可显示（三次复审 §51）：把 policy.users 合并进
  // options——搜索/已加载页之外已有授权不再退化成 raw id。目录候选来自
  // useDirectoryUsers（服务端搜索 + 分页），不再是一次性 limit:100。
  const userOptions = useMemo(() => {
    const byId = new Map<number, { value: number; label: string }>();
    for (const u of policy?.users ?? []) {
      byId.set(u.directory_user_id, {
        value: u.directory_user_id,
        label: u.departments?.length
          ? `${u.name} · ${u.departments.join(" / ")}`
          : u.name,
      });
    }
    for (const u of directoryUsers.items) {
      if (byId.has(u.id)) continue;
      byId.set(u.id, {
        value: u.id,
        label: `${u.name} · ${u.departments.map((d) => d.name).join(" / ")}`,
      });
    }
    return Array.from(byId.values());
  }, [policy, directoryUsers.items]);

  const setUserIds = (ids: number[]) => {
    const pool = new Map<number, {
      directory_user_id: number;
      name: string;
      avatar_url: string;
      departments: string[];
    }>();
    for (const u of policy?.users ?? []) {
      pool.set(u.directory_user_id, {
        directory_user_id: u.directory_user_id,
        name: u.name,
        avatar_url: u.avatar_url,
        departments: u.departments ?? [],
      });
    }
    for (const u of directoryUsers.items) {
      if (pool.has(u.id)) continue;
      pool.set(u.id, {
        directory_user_id: u.id,
        name: u.name,
        avatar_url: u.avatar_url,
        departments: u.departments.map((d) => d.name),
      });
    }
    setUserGrants(ids.map((id) => pool.get(id)!).filter(Boolean));
  };
  const columns: ColumnsType<V2Application> = [
    { title: "资源", dataIndex: "name" },
    { title: "Slug", dataIndex: "slug" },
    {
      title: "操作",
      render: (_, app) => (
        <Button onClick={() => open(app)}>设置权限</Button>
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
        onClose={() => setSelected(null)}
        width={620}
        extra={
          <Button
            type="primary"
            loading={saving}
            disabled={!ready}
            onClick={() => void save()}
          >
            保存
          </Button>
        }
      >
        {policyLoadError ? (
          /* 与 MobilePermissionEditor 同款三态：失败可原地重试，保存保持禁用。 */
          <Alert
            type="error"
            showIcon
            message="加载访问权限失败"
            action={
              <Button size="small" onClick={() => void reload()}>
                重试
              </Button>
            }
          />
        ) : policyLoading || !policy ? (
          /* 首帧（policy 尚未落地）也走 Skeleton —— Empty 只属于确认无数据。 */
          <Skeleton active />
        ) : (
          <Space direction="vertical" size="large" style={{ width: "100%" }}>
            <div>
              <Typography.Title level={5}>访问范围</Typography.Title>
              <Radio.Group
                value={policy.access_mode}
                onChange={(e) => setAccessMode(e.target.value as AccessMode)}
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
                    onChange={(ids) => setDepartmentIds(ids as number[], includeChildren)}
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
                              patchDepartmentGrant(grant.department_id, checked)
                            }
                          />
                        </Space>
                      </Card>
                    ))}
                  </Space>
                </div>
                <div>
                  <Typography.Title level={5}>授权人员</Typography.Title>
                  {/* 与 MobileUserPicker 同一契约（四次复审 P2-2）：搜索词直发
                      后端（覆盖全部员工而非已加载页），下拉滚到底拉下一页，
                      >100 人的搜索结果也能全部选到。 */}
                  <Select
                    mode="multiple"
                    showSearch
                    filterOption={false}
                    loading={directoryUsers.loading}
                    onSearch={setUserQuery}
                    onPopupScroll={(e) => {
                      const { scrollTop, scrollHeight, clientHeight } =
                        e.currentTarget;
                      if (
                        directoryUsers.hasMore
                        && !directoryUsers.loadingMore
                        && scrollHeight - scrollTop - clientHeight < 24
                      ) {
                        void directoryUsers.loadMore();
                      }
                    }}
                    value={policy.users.map((u) => u.directory_user_id)}
                    onChange={setUserIds}
                    style={{ width: "100%" }}
                    options={userOptions}
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
