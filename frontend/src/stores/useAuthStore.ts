import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import axiosInstance from '@/services/axios';

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
  login: (username: string, password: string) => Promise<void>;
  /** /login/admin 入口：POST /api/identity/admin/login（本地管理员）。 */
  adminLogin: (username: string, password: string) => Promise<void>;
  register: (data: RegisterData) => Promise<void>;
  completeSso: (exchange: string) => Promise<void>;
  logout: () => Promise<void>;
  clearAuth: () => void;
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

      login: async (username: string, password: string) => {
        // Cookie session: the HttpOnly `studio_session` cookie is set by the
        // backend (Set-Cookie); no token is stored client-side.
        const response = await axiosInstance.post('/auth/login/', { username, password }) as any;
        set({ user: response.user, isAuthenticated: true });
      },

      adminLogin: async (username: string, password: string) => {
        // 管理员本地登录：与飞书登录共享同一套 HttpOnly Studio Session。
        const response = await axiosInstance.post('/identity/admin/login', { username, password }) as any;
        set({
          user: {
            id: String(response.id ?? ''),
            username: response.username ?? username,
            email: '',
            role: response.is_staff ? 'admin' : 'user',
            is_staff: Boolean(response.is_staff),
            auth_source: response.auth_source ?? 'local_admin',
            display_name: response.display_name ?? response.username,
            display_id: response.display_id ?? '',
            created_at: new Date().toISOString(),
          },
          isAuthenticated: true,
        });
      },

      register: async (data: RegisterData) => {
        const response = await axiosInstance.post('/auth/register/', data) as any;
        set({ user: response.user, isAuthenticated: true });
      },

      completeSso: async (exchange: string) => {
        const response = await axiosInstance.post('/enterprise/sso/exchange', { exchange }) as any;
        set({ user: response.user, isAuthenticated: true });
      },

      clearAuth: () => {
        // Synchronously wipe local auth state. Used right before a full-page
        // redirect to login.
        set({ user: null, isAuthenticated: false });
      },

      logout: async () => {
        get().clearAuth();
        try {
          // Clears the studio_session cookie server-side (immediate revoke).
          await axiosInstance.post('/auth/logout/', {});
        } catch (error) {
          console.error('Logout error:', error);
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
