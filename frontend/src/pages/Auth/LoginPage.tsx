import React from 'react';
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
  return (
    <div className="animate-fade-in-scale rounded-xl border border-border bg-card/80 p-8 shadow-lg backdrop-blur-xl">
      <div className="mb-8 text-center">
        <h1 className="animate-logo-reveal font-display text-2xl font-bold text-primary">
          Creation Studio
        </h1>
        <Text className="mt-2 block text-text-sec">使用飞书账号登录</Text>
      </div>

      <Button
        type="primary"
        block
        icon={<span aria-hidden>🪶</span>}
        className="h-10 rounded-lg border-none font-medium"
        style={{
          background: 'var(--color-primary)',
          boxShadow: '0 0 20px rgba(232, 168, 56, 0.3)',
        }}
        onClick={() => { window.location.href = '/api/identity/oauth/start'; }}
      >
        飞书登录
      </Button>

      <Alert
        style={{ marginTop: 24 }}
        type="info"
        showIcon
        message="本系统使用飞书统一身份认证"
        description="普通用户无需注册账号，使用企业飞书扫码即可进入。"
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
