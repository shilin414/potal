import React, { useState } from 'react';
import { Button, Form, Input, message } from 'antd';
import { LockOutlined, UserOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useAuthStore } from '@/stores/useAuthStore';

/**
 * /login/admin — 管理员本地账号登录（架构准则 §46）。
 *
 * 普通用户界面不出现管理员登录入口；本页通过 /api/identity/admin/login
 * 验证本地管理员口令（Argon2id），成功后写入与飞书登录同一套
 * HttpOnly Studio Session。
 */
const AdminLoginPage: React.FC = () => {
  const [loading, setLoading] = useState(false);
  const navigate = useNavigate();
  const adminLogin = useAuthStore((state) => state.adminLogin);

  const onFinish = async (values: { username: string; password: string }) => {
    setLoading(true);
    try {
      await adminLogin(values.username, values.password);
      message.success('管理员登录成功');
      navigate('/', { replace: true });
    } catch (error: any) {
      const errData = error.response?.data;
      message.error(errData?.detail || errData?.error || '登录失败');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="animate-fade-in-scale rounded-xl border border-border bg-card/80 p-8 shadow-lg backdrop-blur-xl">
      <div className="mb-8 text-center">
        <h1 className="animate-logo-reveal font-display text-2xl font-bold text-primary">
          Creation Studio
        </h1>
        <p className="mt-2 block text-text-sec">管理员登录</p>
      </div>

      <Form name="admin-login" onFinish={onFinish} autoComplete="off" size="large" layout="vertical">
        <Form.Item name="username" rules={[{ required: true, message: '请输入管理员用户名' }]}>
          <Input prefix={<UserOutlined className="text-text-dim" />} placeholder="管理员用户名" className="rounded-lg" />
        </Form.Item>
        <Form.Item name="password" rules={[{ required: true, message: '请输入密码' }]}>
          <Input.Password prefix={<LockOutlined className="text-text-dim" />} placeholder="密码" className="rounded-lg" />
        </Form.Item>
        <Form.Item>
          <Button
            type="primary"
            htmlType="submit"
            block
            loading={loading}
            className="animate-glow-pulse h-10 rounded-lg border-none font-medium"
            style={{
              background: 'var(--color-primary)',
              color: 'var(--color-on-primary)',
              boxShadow: '0 0 20px color-mix(in srgb, var(--color-primary) 30%, transparent)',
            }}
          >
            登录
          </Button>
        </Form.Item>
      </Form>
      <div className="text-center">
        <a href="/login" className="text-text-sec hover:underline" style={{ fontSize: 12 }}>
          返回飞书登录
        </a>
      </div>
    </div>
  );
};

export default AdminLoginPage;
