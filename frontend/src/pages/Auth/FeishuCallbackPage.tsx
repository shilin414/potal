import { useEffect, useRef } from 'react';
import { Alert, Spin } from 'antd';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { completeFeishuLogin } from '@/services/feishuAuth';

/**
 * Feishu OAuth 回调页。
 *
 * 飞书授权完成后重定向到 /auth/feishu/callback?code=...&state=...，
 * 本页面把 code 交给后端 /api/identity/oauth/exchange，换取：
 *  - Studio Session（httpOnly cookie，由后端 Set-Cookie 完成）
 *  - JWT access/refresh（写入 authStore，供现有 axios 拦截器使用）
 * 然后进入主工作台。
 */
export default function FeishuCallbackPage() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const started = useRef(false);

  const code = params.get('code');
  const state = params.get('state') || '';
  const error = params.get('error');

  useEffect(() => {
    if (started.current || !code) return;
    started.current = true;
    completeFeishuLogin(code, state)
      .then(() => navigate('/', { replace: true }))
      .catch(() => navigate('/auth/login?feishu_error=1', { replace: true }));
  }, [code, state, navigate]);

  if (error) {
    return (
      <Alert
        type="error"
        message="飞书授权失败"
        description={error}
        showIcon
      />
    );
  }
  if (!code) {
    return (
      <Alert
        type="error"
        message="无效的飞书回调"
        description="缺少授权码，请从登录入口重新发起飞书登录。"
        showIcon
      />
    );
  }
  return (
    <div style={{ textAlign: 'center', paddingTop: 120 }}>
      <Spin size="large" />
      <p style={{ marginTop: 24 }}>正在完成飞书登录…</p>
    </div>
  );
}
