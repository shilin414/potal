/**
 * SyncPage — desktop 同步管理（原样搬移自 EnterprisePage.tsx）。
 */
import React, { useCallback, useEffect, useState } from "react";
import {
  Alert,
  Button,
  Card,
  Form,
  Input,
  InputNumber,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  TimePicker,
  message,
} from "antd";
import dayjs from "dayjs";
import { enterpriseApi, type SyncConfig, type SyncRun } from "../enterpriseApi";
import { fmt } from "../enterpriseNav";

export default function SyncPage() {
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
