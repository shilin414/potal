/**
 * Overview — desktop 概览（原样搬移自 EnterprisePage.tsx）。
 */
import React, { useEffect, useState } from 'react';
import { Alert, Card, Col, Row, Statistic } from 'antd';
import { enterpriseApi, type SyncRun } from '../enterpriseApi';

export default function Overview() {
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
