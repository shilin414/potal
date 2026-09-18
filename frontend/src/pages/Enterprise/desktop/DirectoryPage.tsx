/**
 * DirectoryPage — desktop 部门与人员（原样搬移自 EnterprisePage.tsx）。
 */
import React, { useCallback, useEffect, useState } from "react";
import { Avatar, Card, Col, Input, Row, Space, Table, Tag } from "antd";
import {
  enterpriseApi,
  type DirectoryDepartment,
  type DirectoryUser,
} from "../enterpriseApi";

export default function DirectoryPage() {
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
