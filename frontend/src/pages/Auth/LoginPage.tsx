import React from 'react';
import { useSearchParams } from 'react-router-dom';
import { Alert, Button, Typography } from 'antd';

const { Text } = Typography;

/**
 * /auth/login — 飞书登录 fallback 页（架构准则：Feishu SSO Only）。
 *
 * 正常流程中普通用户永远不落到这里：未登录访问会经 /login 自动发起
 * 飞书 OAuth。本页只在 OAuth 回调失败（/auth/feishu/callback?feishu_error=1）
 * 时作为重试入口 —— 没有用户名密码、没有注册、没有企业 SSO。
 * 管理员入口独立在 /login/admin。
 */
const LoginPage: React.FC = () => {
  const [searchParams] = useSearchParams();
  const loggedOut = searchParams.get('logged_out') === '1';

  return (
    <div className="animate-fade-in-scale rounded-xl border border-border bg-card/80 p-8 shadow-lg backdrop-blur-xl">
      <div className="mb-8 text-center">
        <h1 className="animate-logo-reveal font-display text-2xl font-bold text-primary">
          Creation Studio
        </h1>
        <Text className="mt-2 block text-text-sec">
          {loggedOut ? '已安全退出' : '使用飞书账号登录'}
        </Text>
      </div>

      <Button
        type="primary"
        block
        icon={<span aria-hidden>🪶</span>}
        className="h-10 rounded-lg border-none font-medium"
        style={{
          background: 'var(--color-primary)',
          color: 'var(--color-on-primary)',
          boxShadow: '0 0 20px color-mix(in srgb, var(--color-primary) 30%, transparent)',
        }}
        onClick={() => { window.location.href = '/api/identity/oauth/start'; }}
      >
        飞书登录
      </Button>

      <Alert
        style={{ marginTop: 24 }}
        type="info"
        showIcon
        message={loggedOut ? '你已退出当前账号' : '本系统使用飞书统一身份认证'}
        description={
          loggedOut
            ? '如需继续使用，请主动选择飞书登录或管理员登录。'
            : '普通用户无需注册账号，使用企业飞书扫码即可进入。'
        }
      />

      <div className="mt-6 text-center">
        <a href="/login/admin" className="text-text-dim hover:underline" style={{ fontSize: 12 }}>
          管理员登录
        </a>
      </div>
    </div>
  );
};

export default LoginPage;
