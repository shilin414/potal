import React, { useState } from 'react';
import { Divider, Form, Input, Button, message, Modal, Select, Typography } from 'antd';
import { api } from '@/services/api';
import { UserOutlined, LockOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useAuthStore } from '@/stores/useAuthStore';

const { Text } = Typography;

const LoginPage: React.FC = () => {
  const [loading, setLoading] = useState(false);
  const [ssoOpen, setSsoOpen] = useState(false);
  const [ssoProviders, setSsoProviders] = useState<Array<{ id: number; name: string; organization: string; login_url: string }>>([]);
  const [ssoProvider, setSsoProvider] = useState<number>();
  const navigate = useNavigate();
  const login = useAuthStore((state) => state.login);

  const onFinish = async (values: { username: string; password: string }) => {
    setLoading(true);
    try {
      await login(values.username, values.password);
      message.success('登录成功');
      navigate('/');
    } catch (error: any) {
      const errData = error.response?.data;
      if (errData?.detail) {
        message.error(errData.detail);
      } else if (errData && typeof errData === 'object') {
        const firstError = Object.values(errData)[0];
        if (Array.isArray(firstError)) message.error(firstError[0]);
        else message.error('登录失败');
      } else {
        message.error('登录失败');
      }
    } finally {
      setLoading(false);
    }
  };

  const discoverSso = async (values: { email: string }) => {
    const domain = values.email.split('@')[1];
    const providers = await api.get<typeof ssoProviders>('/enterprise/sso/discovery', { domain });
    setSsoProviders(providers); setSsoProvider(providers[0]?.id);
    if (!providers.length) message.warning('该邮箱域名未配置企业身份提供商');
  };

  return (
    <div className="animate-fade-in-scale rounded-xl border border-border bg-card/80 p-8 shadow-lg backdrop-blur-xl">
      <div className="mb-8 text-center">
        <h1 className="animate-logo-reveal font-display text-2xl font-bold text-primary">
          Creation Studio
        </h1>
        <Text className="mt-2 block text-text-sec">登录到您的账户</Text>
      </div>

      <Form name="login" onFinish={onFinish} autoComplete="off" size="large" layout="vertical">
        <Form.Item name="username" rules={[{ required: true, message: '请输入用户名' }]}>
          <Input
            prefix={<UserOutlined className="text-text-dim" />}
            placeholder="用户名"
            className="rounded-lg"
          />
        </Form.Item>

        <Form.Item name="password" rules={[{ required: true, message: '请输入密码' }]}>
          <Input.Password
            prefix={<LockOutlined className="text-text-dim" />}
            placeholder="密码"
            className="rounded-lg"
          />
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
              boxShadow: '0 0 20px rgba(232, 168, 56, 0.3)',
            }}
          >
            登录
          </Button>
        </Form.Item>

        <Divider plain>或</Divider>
        <Button block onClick={() => setSsoOpen(true)}>企业 SSO 登录</Button>

        <div className="text-center">
          <Text className="text-text-sec">
            还没有账户？{' '}
            <a href="/auth/register" className="text-primary hover:underline">立即注册</a>
          </Text>
        </div>
      </Form>
      <Modal title="企业 SSO 登录" open={ssoOpen} onCancel={() => setSsoOpen(false)} okText="前往企业登录" okButtonProps={{ disabled: !ssoProvider }} onOk={() => { const provider = ssoProviders.find(item => item.id === ssoProvider); if (provider) window.location.assign(provider.login_url); }}>
        <Form layout="vertical" onFinish={discoverSso}><Form.Item name="email" label="企业邮箱" rules={[{ required: true, type: 'email' }]}><Input /></Form.Item><Button htmlType="submit">查找身份提供商</Button></Form>
        {ssoProviders.length > 0 && <Select style={{ width: '100%', marginTop: 16 }} value={ssoProvider} onChange={setSsoProvider} options={ssoProviders.map(item => ({ value: item.id, label: `${item.organization} · ${item.name}` }))} />}
      </Modal>
    </div>
  );
};

export default LoginPage;
