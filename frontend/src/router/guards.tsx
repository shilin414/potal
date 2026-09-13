import { Navigate } from 'react-router-dom';
import { useAuthStore } from '@/stores/useAuthStore';

interface ProtectedRouteProps {
  children: React.ReactNode;
}

/**
 * 未登录的普通用户不进入任何「登录页」：直接引导到 /login，
 * 由该路由自动发起飞书 OAuth（架构准则 §43：Feishu SSO Only，
 * 用户甚至不应该看到登录页）。
 */
export const ProtectedRoute: React.FC<ProtectedRouteProps> = ({ children }) => {
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);

  if (!isAuthenticated) {
    return <Navigate to="/login" replace />;
  }

  return <>{children}</>;
};

export const PublicRoute: React.FC<ProtectedRouteProps> = ({ children }) => {
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);

  if (isAuthenticated) {
    return <Navigate to="/" replace />;
  }

  return <>{children}</>;
};
