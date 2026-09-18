/**
 * ProvidersPage — desktop Provider（原样搬移自 EnterprisePage.tsx）。
 */
import React, { useEffect, useState } from "react";
import { Table } from "antd";
import { fetchAgentRuntimes } from "@/services/runApi";

export default function ProvidersPage() {
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
