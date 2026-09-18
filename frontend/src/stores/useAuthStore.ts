import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import axiosInstance from '@/services/axios';
import {
  broadcastExplicitLogout,
  broadcastIdentityChange,
  subscribeExplicitLogout,
} from '@/stores/authBoundary';
import {
  isSameUser,
  resetSessionScopedState,
} from '@/stores/resetSessionState';

interface User {
  id: string;
  username: string;
  email: string;
  role: string;
  avatar?: string;
  /** Server-side avatar URL from the identity snapshot (preferred). */
  avatar_url?: string;
  /** 展示名（飞书昵称 / first_name / username）。 */
  display_name?: string;
  /** 展示用 user_id（工号 / 飞书 user_id）；聊天里渲染成 姓名（user_id）。 */
  display_id?: string;
  auth_source?: string;
  /** 管理员：可新建智能体、切换应用中心的 启用/公开 开关。 */
  is_staff?: boolean;
  bio?: string;
  created_at: string;
}

interface AuthState {
  user: User | null;
  isAuthenticated: boolean;
  isLoggingOut: boolean;
  explicitlyLoggedOut: boolean;
  login: (username: string, password: string) => Promise<void>;
  /** /login/admin 入口：POST /api/identity/admin/login（本地管理员）。 */
  adminLogin: (username: string, password: string) => Promise<void>;
  register: (data: RegisterData) => Promise<void>;
  completeSso: (exchange: string) => Promise<void>;
  logout: () => Promise<void>;
  clearAuth: () => void;
  /**
   * The ONLY way an authenticated identity is installed (二次复审 P0-2).
   *
   * Every login path (password, admin, SSO, Feishu) ends here instead of
   * calling `set({ user })` directly, so the session-scoped stores are wiped
   * exactly once, in exactly one place — before the new identity is
   * installed. A page that sets `user` by hand is a page that leaks the
   * previous user's drafts, transcripts and shortcuts to the next one.
   */
  acceptAuthenticatedUser: (user: User) => void;
  updateUser: (data: Partial<User>) => void;
}

interface RegisterData {
  username: string;
  email: string;
  password: string;
  password_confirm: string;
  role?: string;
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set, get) => ({
      user: null,
      isAuthenticated: false,
      isLoggingOut: false,
      explicitlyLoggedOut: false,

      login: async (username: string, password: string) => {
        // Cookie session: the HttpOnly `studio_session` cookie is set by the
        // backend (Set-Cookie); no token is stored client-side.
        const response = await axiosInstance.post('/auth/login/', { username, password }) as any;
        get().acceptAuthenticatedUser(response.user);
      },

      adminLogin: async (username: string, password: string) => {
        // 管理员本地登录：与飞书登录共享同一套 HttpOnly Studio Session。
        const response = await axiosInstance.post('/identity/admin/login', { username, password }) as any;
        get().acceptAuthenticatedUser({
          id: String(response.id ?? ''),
          username: response.username ?? username,
          email: '',
          role: response.is_staff ? 'admin' : 'user',
          is_staff: Boolean(response.is_staff),
          auth_source: response.auth_source ?? 'local_admin',
          display_name: response.display_name ?? response.username,
          display_id: response.display_id ?? '',
          created_at: new Date().toISOString(),
        });
      },

      register: async (data: RegisterData) => {
        const response = await axiosInstance.post('/auth/register/', data) as any;
        get().acceptAuthenticatedUser(response.user);
      },

      completeSso: async (exchange: string) => {
        const response = await axiosInstance.post('/enterprise/sso/exchange', { exchange }) as any;
        get().acceptAuthenticatedUser(response.user);
      },

      clearAuth: () => {
        // Synchronously wipe local auth state AND every session-scoped store.
        // Used right before a full-page redirect to login.
        resetSessionScopedState();
        set({
          user: null,
          isAuthenticated: false,
          isLoggingOut: false,
          explicitlyLoggedOut: false,
        });
      },

      acceptAuthenticatedUser: (user) => {
        // A DIFFERENT identity ends the previous session's state first
        // (二次复审 P0-2). Same identity (a re-login, a token refresh that
        // re-installs the snapshot) must NOT: that would throw away the
        // user's own composer draft and open conversation.
        const identityChanged = !isSameUser(get().user, user);
        if (identityChanged) {
          resetSessionScopedState();
        }
        set({
          user,
          isAuthenticated: true,
          isLoggingOut: false,
          explicitlyLoggedOut: false,
        });
        // Other tabs share the HttpOnly cookie but keep separate in-memory
        // stores. Make them fail closed and revalidate immediately.
        if (identityChanged) broadcastIdentityChange(String(user.id));
      },

      logout: async () => {
        if (get().isLoggingOut) return;
        // Keep the authenticated shell mounted while the cookie revoke is in
        // flight. Clearing auth first lets ProtectedRoute mount /login, whose
        // auto-OAuth navigation can abort this request and immediately log the
        // user back in.
        set({ isLoggingOut: true });
        // Notify sibling tabs before the revoke request can invalidate the
        // shared cookie and make their in-flight requests return 401.
        broadcastExplicitLogout();
        try {
          await axiosInstance.post('/auth/logout/', {}, { timeout: 5000 });
        } catch (error) {
          // A transport failure must not trap the user in the old local
          // session. We still finish the local boundary in finally.
          console.error('Logout error:', error);
        } finally {
          completeExplicitLogout();
        }
      },

      updateUser: (data: Partial<User>) => {
        const { user } = get();
        if (user) {
          set({ user: { ...user, ...data } });
        }
      },
    }),
    {
      name: 'auth-storage',
      partialize: (state) => ({
        user: state.user,
        isAuthenticated: state.isAuthenticated,
      }),
    }
  )
);

function completeExplicitLogout(): void {
  resetSessionScopedState();
  useAuthStore.setState({
    user: null,
    isAuthenticated: false,
    isLoggingOut: false,
    explicitlyLoggedOut: true,
  });
}

// Active tabs share the browser cookie but not Zustand memory. A transient
// BroadcastChannel boundary prevents a sibling tab from treating the next 401
// as session expiry and immediately recreating the just-revoked session.
const unsubscribeExplicitLogout = subscribeExplicitLogout(completeExplicitLogout);
const hot = (import.meta as ImportMeta & {
  hot?: { dispose: (callback: () => void) => void };
}).hot;
hot?.dispose(unsubscribeExplicitLogout);
