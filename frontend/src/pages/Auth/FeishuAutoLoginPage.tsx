import { useEffect, useRef } from 'react';
import { Alert, Button, Spin } from 'antd';

/**
 * /login — 普通用户入口：自动发起飞书 OAuth（架构准则：Feishu SSO Only）。
 *
 * 未登录用户访问任何受保护路由都会被引导到这里，本页在挂载后立即
 * 整页跳转到后端 /api/identity/oauth/start。用户永远不应该看到
 * 「用户名密码登录页」——那是 /login/admin 管理员专属入口。
 */
export default function FeishuAutoLoginPage() {
  const started = useRef(false);

  useEffect(() => {
    if (started.current) return;
    started.current = true;
    window.location.href = '/api/identity/oauth/start';
  }, []);

  return (
    <div style={{ textAlign: 'center', paddingTop: 120 }}>
      <Spin size="large" />
      <p style={{ marginTop: 24 }}>正在跳转飞书登录…</p>
      <p style={{ marginTop: 8, color: 'var(--color-text-sec, #888)' }}>
        如果没有自动跳转，请点击下方按钮。
      </p>
      <Button
        type="primary"
        style={{ marginTop: 16 }}
        onClick={() => { window.location.href = '/api/identity/oauth/start'; }}
      >
        使用飞书登录
      </Button>
      <div style={{ marginTop: 32 }}>
        <a href="/login/admin" style={{ color: 'var(--color-text-dim, #aaa)', fontSize: 12 }}>
          管理员登录
        </a>
      </div>
      <Alert
        style={{ maxWidth: 420, margin: '32px auto 0', textAlign: 'left' }}
        type="info"
        showIcon
        message="本系统使用飞书统一身份登录"
        description="普通用户无需账号密码。管理员请使用管理员入口。"
      />
    </div>
  );
}
