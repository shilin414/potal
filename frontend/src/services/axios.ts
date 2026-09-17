import axios from 'axios';
import { message } from 'antd';
import { useAuthStore } from '@/stores/useAuthStore';

const axiosInstance = axios.create({
  // Default '/api' matches the backend URL layout (every endpoint lives
  // under /api/) and the Vite dev-server proxy. Override with
  // VITE_API_BASE_URL (e.g. a full origin) for other deployments.
  baseURL: import.meta.env.VITE_API_BASE_URL || '/api',
  timeout: 10000,
  withCredentials: true, // cookie session: studio_session
  headers: {
    'Content-Type': 'application/json',
  },
});

// CSRF: double-submit — the readable studio_csrf cookie must be echoed in
// the X-CSRF-Token header on every state-changing request.
const getCsrfToken = (): string => {
  const match = document.cookie.match(/(?:^|;\s*)studio_csrf=([^;]*)/);
  return match ? decodeURIComponent(match[1]) : '';
};

// Request interceptor
axiosInstance.interceptors.request.use(
  (config) => {
    // FormData must not inherit the instance-level JSON content type.
    // axios decides "is this a JSON body?" from the Content-Type header and,
    // when it is json, serializes FormData with `formDataToJSON()` — the
    // multipart body never leaves the browser and the server sees no files
    // (this silently broke every upload: 附件预上传 and 头像上传).
    // Dropping the header lets the browser set `multipart/form-data; boundary=`.
    if (typeof FormData !== 'undefined' && config.data instanceof FormData) {
      delete config.headers['Content-Type'];
      delete config.headers['content-type'];
    }
    const method = (config.method || 'get').toLowerCase();
    if (method !== 'get' && method !== 'head' && method !== 'options') {
      const csrf = getCsrfToken();
      if (csrf) {
        config.headers['X-CSRF-Token'] = csrf;
      }
    }
    let organizationId: string | null = null;
    try {
      const persisted = JSON.parse(localStorage.getItem('organization-storage') || '{}');
      organizationId = persisted?.state?.currentOrganizationId || null;
    } catch {
      organizationId = null;
    }
    if (organizationId) {
      config.headers['X-Organization-ID'] = organizationId;
    }
    return config;
  },
  (error) => {
    return Promise.reject(error);
  }
);

// Force re-login: synchronously clear local auth state, then redirect to the
// login page. Guarded so a burst of simultaneous 401s only redirects once.
let redirectingToLogin = false;
const redirectToLogin = () => {
  if (redirectingToLogin) return;
  redirectingToLogin = true;
  // clearAuth() also wipes the session-scoped stores (二次复审 P0-2): an
  // expired session is an identity boundary, and leaving the previous
  // user's drafts / transcripts / shortcuts in memory until a new login
  // replaces them is the leak this fixes.
  useAuthStore.getState().clearAuth();
  window.location.href = '/auth/login';
};

// Endpoints that own their own 401s (wrong password): we must NOT redirect
// for these — surface the error to the caller (login form).
const AUTH_PATHS = ['/auth/login/', '/auth/register/', '/auth/logout/'];

axiosInstance.interceptors.response.use(
  (response) => {
    return response.data;
  },
  (error) => {
    const originalRequest = error.config;
    const status = error.response?.status;
    const isAuthRequest = AUTH_PATHS.some((p) => originalRequest?.url?.includes(p));

    // 401 on an auth endpoint (login/register/logout): hand it back to the
    // caller (e.g. the login form showing "wrong password").
    if (status === 401 && isAuthRequest) {
      return Promise.reject(error);
    }

    // 401 elsewhere: the cookie session is gone/expired — re-login.
    if (status === 401) {
      redirectToLogin();
      return Promise.reject(error);
    }

    if (error.response) {
      switch (status) {
        case 403:
          message.error('拒绝访问');
          break;
        case 404:
          message.error('请求错误，未找到该资源');
          break;
        case 500:
          message.error('服务器错误');
          break;
        default:
          message.error(error.response.data?.detail || '请求失败');
      }
    }

    return Promise.reject(error);
  }
);

export default axiosInstance;
