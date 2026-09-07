import { useEffect, useRef } from 'react';
import { Alert, Spin } from 'antd';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useAuthStore } from '@/stores/useAuthStore';

export default function SsoCallbackPage() {
  const [params] = useSearchParams(); const navigate = useNavigate();
  const completeSso = useAuthStore(state => state.completeSso);
  const started = useRef(false); const exchange = params.get('exchange');
  useEffect(() => {
    if (started.current || !exchange) return; started.current = true;
    void completeSso(exchange).then(() => navigate('/enterprise', { replace: true })).catch(() => navigate('/auth/login?sso_error=1', { replace: true }));
  }, [completeSso, exchange, navigate]);
  if (!exchange) return <Alert type="error" message="无效的 SSO 回调" />;
  return <div style={{ textAlign: 'center' }}><Spin size="large" /><p>正在完成企业登录…</p></div>;
}
