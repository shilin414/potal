/**
 * AuditPage — desktop 审计日志（原样搬移自 EnterprisePage.tsx）。
 */
import React, { useEffect, useState } from "react";
import { Table, Typography } from "antd";
import { enterpriseApi, type AuditLog } from "../enterpriseApi";
import { fmt } from "../enterpriseNav";

export default function AuditPage() {
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
