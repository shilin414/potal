import axiosInstance from './axios';
import { useAuthStore } from '@/stores/useAuthStore';

/**
 * 飞书 OAuth code 兑换 + Studio Session 建立。
 *
 * 后端 /api/identity/oauth/exchange：
 *  - 用 code 换 user_access_token
 *  - 映射/创建本地用户（auth_source=feishu）
 *  - 签发 studio_session cookie（httpOnly，用于 /api/v2 统一 Run API）
 *  - 同时返回 JWT（兼容现有前端 axios 拦截器）
 */
export async function completeFeishuLogin(code: string, state: string): Promise<void> {
  const response = await axiosInstance.get('/identity/oauth/exchange', {
    params: { code, state },
  }) as any;

  const { user } = response || {};
  if (!user) {
    throw new Error('feishu exchange returned no user');
  }

  // Cookie session: studio_session is set by the backend; no client token.
  //
  // Through `acceptAuthenticatedUser`, never `setState` (二次复审 P0-2): a
  // Feishu login is exactly the user switch whose session-scoped state used
  // to survive — the bootstrap, the entity cache, the drafts, the
  // transcripts — because this path only ever replaced the user object.
  useAuthStore.getState().acceptAuthenticatedUser(user);
}
