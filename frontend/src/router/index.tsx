import { lazy, Suspense, type ReactNode } from 'react';
import { createBrowserRouter, Navigate } from 'react-router-dom';
import { AuthLayout } from '@/layouts';
import AppShell from '@/shell/AppShell';
import WorkspaceHost from '@/components/Workspace/WorkspaceHost';
import { AdminRoute, ProtectedRoute, PublicRoute } from './guards';
import LegacyAppRunRedirect from './LegacyAppRunRedirect';

// Console pages (application management) are route-level lazy (三次复审
// §55–§58): 首页 / Chat 首屏不再下载用户可能永远不会进入的管理页 ——
// Enterprise 已用同一模式证明可行。只切分次级 console 路由：
// WorkspaceHost / 主 Chat / Shell / 登录页 保持同步加载。
const TaskCenterPage = lazy(() => import('@/workbench/tasks/TaskCenterPage'));
const AgentsPage = lazy(() => import('@/pages/Agents/AgentsPage'));
const AgentDetailPage = lazy(() => import('@/pages/Agents/AgentDetailPage'));
const TemplatesPage = lazy(() => import('@/pages/Templates/TemplatesPage'));
const TemplateDetailPage = lazy(() => import('@/pages/Templates/TemplateDetailPage'));
const AppsPage = lazy(() => import('@/pages/Apps/AppsPage'));
const AppDetailPage = lazy(() => import('@/pages/Apps/AppDetailPage'));
const ChatApplicationEditPage = lazy(() => import('@/pages/Apps/ChatApplicationEditPage'));
const WorkspacePage = lazy(() => import('@/pages/Workspace/WorkspacePage'));
const WorkflowsPage = lazy(() => import('@/pages/Workflows/WorkflowsPage'));
const WorkflowEditorPage = lazy(() => import('@/pages/Workflows/WorkflowEditorPage'));
const WorkflowRunnerPage = lazy(() => import('@/pages/Workflows/WorkflowRunnerPage'));
const SkillsPage = lazy(() => import('@/pages/Skills/SkillsPage'));
const SchedulesPage = lazy(() => import('@/pages/Schedules/SchedulesPage')
  .then((m) => ({ default: m.SchedulesPage })));

// Auth pages stay outside the shell entirely.
// 普通用户 = 飞书 SSO Only（/login 自动发起 OAuth）；管理员 = /login/admin。
import LoginPage from '@/pages/Auth/LoginPage';
import FeishuAutoLoginPage from '@/pages/Auth/FeishuAutoLoginPage';
import AdminLoginPage from '@/pages/Auth/AdminLoginPage';
import SsoCallbackPage from '@/pages/Auth/SsoCallbackPage';
import FeishuCallbackPage from '@/pages/Auth/FeishuCallbackPage';

// Public share snapshot (no shell, no auth — the backend returns only the
// snapshotted messages for the token).
import SharePage from '@/pages/Share/SharePage';

const EnterprisePage = lazy(() => import('@/pages/Enterprise/EnterprisePage'));

const enterpriseElement = (
  <Suspense fallback={<div style={{ padding: 32 }}>正在加载企业控制台…</div>}>
    <AdminRoute><EnterprisePage /></AdminRoute>
  </Suspense>
);

/** 次级 console 路由的懒加载壳：短 fallback，不打断布局。 */
const lazyConsole = (node: ReactNode) => (
  <Suspense fallback={<div style={{ padding: 32 }}>正在加载…</div>}>{node}</Suspense>
);

/** Console pages scroll and pad; workspaces lay themselves out (§17/§33). */
const consolePage = { shell: { padded: true } };
const fullWidthConsole = { shell: { padded: true } };
const fullscreenConsole = { shell: { hideSidebar: true, hideHeader: true } };

// Mobile shell handles (开发执行报告 §6): the header swaps the workspace
// switcher for a page title; Schedules gains a create action wired by the
// page itself via useMobileHeader. Enterprise sub-pages override the title
// dynamically (mode: 'detail') from EnterprisePage's mobile branch.
const fixedWorkspaceHandle = {
  shell: { mobile: { mode: 'page' as const, title: '应用', action: 'none' as const } },
};
const taskPageHandle = {
  shell: {
    padded: false,
    mobile: { mode: 'page' as const, title: '任务', action: 'none' as const },
  },
};
const agentsPageHandle = {
  shell: {
    padded: true,
    mobile: { mode: 'page' as const, title: '智能体中心' },
  },
};
const appsPageHandle = {
  shell: {
    padded: true,
    mobile: { mode: 'page' as const, title: '应用中心' },
  },
};
const schedulesPageHandle = {
  shell: {
    padded: true,
    mobile: { mode: 'page' as const, title: '自动化', action: 'create' as const },
  },
};
const enterprisePageHandle = {
  shell: {
    padded: true,
    mobile: { mode: 'console' as const, title: '企业控制台' },
  },
};

/**
 * One AppShell wraps every authenticated route (§33).
 *
 * The shell is a *layout* route, so switching between the main workspace and
 * the console never unmounts it — only <Outlet/> swaps. Workspace routes
 * (`/`, `/chat/:slug`, `/app/:slug`, `/workflow/:slug`) all render the same
 * WorkspaceHost, which is why a fixed application opens inside the shell
 * instead of navigating away (§32).
 */
const router = createBrowserRouter([
  {
    path: '/',
    element: (
      <ProtectedRoute>
        <AppShell />
      </ProtectedRoute>
    ),
    children: [
      // ── Workspaces (§18/§33) ──────────────────────────────────────────
      { index: true, element: <WorkspaceHost kind="home" /> },
      { path: 'tasks', element: lazyConsole(<TaskCenterPage />), handle: taskPageHandle },
      { path: 'chat/:applicationSlug', element: <WorkspaceHost kind="chat" /> },
      { path: 'app/:applicationSlug', element: <WorkspaceHost kind="page" />, handle: fixedWorkspaceHandle },
      {
        path: 'workflow/:applicationSlug',
        element: <WorkspaceHost kind="workflow" />,
        handle: fixedWorkspaceHandle,
      },

      // ── Console (application management, route-level lazy) ────────────
      { path: 'agents', element: lazyConsole(<AgentsPage />), handle: agentsPageHandle },
      {
        path: 'agents/:id',
        element: lazyConsole(<AgentDetailPage />),
        handle: consolePage,
      },
      {
        path: 'templates',
        element: lazyConsole(<TemplatesPage />),
        handle: consolePage,
      },
      {
        path: 'templates/:id',
        element: lazyConsole(<TemplateDetailPage />),
        handle: consolePage,
      },
      { path: 'apps', element: lazyConsole(<AppsPage />), handle: appsPageHandle },
      {
        path: 'apps/:id',
        element: lazyConsole(
          <AdminRoute><AppDetailPage /></AdminRoute>,
        ),
        handle: consolePage,
      },
      // Launched apps now live in the shell's application workspace (§32).
      { path: 'apps/:id/run', element: <LegacyAppRunRedirect /> },
      {
        path: 'apps/:id/edit',
        element: lazyConsole(
          <AdminRoute><ChatApplicationEditPage /></AdminRoute>,
        ),
        handle: consolePage,
      },
      { path: 'skills', element: lazyConsole(<SkillsPage />), handle: fullWidthConsole },
      {
        path: 'schedules',
        element: lazyConsole(<SchedulesPage />),
        handle: schedulesPageHandle,
      },
      {
        path: 'workflows',
        element: lazyConsole(<WorkflowsPage />),
        handle: fullWidthConsole,
      },
      {
        path: 'workflows/:id/edit',
        element: lazyConsole(<WorkflowEditorPage />),
        handle: fullWidthConsole,
      },
      {
        path: 'workflow-runs/:runId',
        element: lazyConsole(<WorkflowRunnerPage />),
        handle: fullscreenConsole,
      },
      // Template-workflow workspace: still on the legacy agent engine.
      {
        path: 'workspace',
        element: lazyConsole(<WorkspacePage />),
        handle: consolePage,
      },
      {
        path: 'workspace/:id',
        element: lazyConsole(<WorkspacePage />),
        handle: fullWidthConsole,
      },
      { path: 'enterprise/*', element: enterpriseElement, handle: enterprisePageHandle },

      { path: '*', element: <Navigate to="/" replace /> },
    ],
  },
  // ── Auth (no shell) ───────────────────────────────────────────────────
  // /login：普通用户入口，自动发起飞书 OAuth（Feishu SSO Only）。
  {
    path: '/login',
    element: <AuthLayout><PublicRoute><FeishuAutoLoginPage /></PublicRoute></AuthLayout>,
  },
  // /login/admin：管理员本地账号口令登录（唯一出现用户名密码的入口）。
  {
    path: '/login/admin',
    element: <AuthLayout><AdminLoginPage /></AuthLayout>,
  },
  {
    path: '/auth/sso/callback',
    element: <AuthLayout><SsoCallbackPage /></AuthLayout>,
  },
  {
    path: '/auth/feishu/callback',
    element: <AuthLayout><FeishuCallbackPage /></AuthLayout>,
  },
  // /auth/login：飞书登录 fallback（OAuth 失败时的重试入口，无密码/注册）。
  {
    path: '/auth/login',
    element: <AuthLayout><PublicRoute><LoginPage /></PublicRoute></AuthLayout>,
  },
  // ── Public share snapshot (no shell, works logged-out) ─────────────────
  {
    path: '/share/:token',
    element: <SharePage />,
  },
  { path: '*', element: <Navigate to="/" replace /> },
]);

export default router;
