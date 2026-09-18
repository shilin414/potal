import { lazy, Suspense } from 'react';
import { createBrowserRouter, Navigate } from 'react-router-dom';
import { AuthLayout } from '@/layouts';
import AppShell from '@/shell/AppShell';
import WorkspaceHost from '@/components/Workspace/WorkspaceHost';
import { AdminRoute, ProtectedRoute, PublicRoute } from './guards';
import LegacyAppRunRedirect from './LegacyAppRunRedirect';

// Console pages (application management).
import AgentsPage from '@/pages/Agents/AgentsPage';
import AgentDetailPage from '@/pages/Agents/AgentDetailPage';
import TemplatesPage from '@/pages/Templates/TemplatesPage';
import TemplateDetailPage from '@/pages/Templates/TemplateDetailPage';
import AppsPage from '@/pages/Apps/AppsPage';
import AppDetailPage from '@/pages/Apps/AppDetailPage';
import ChatApplicationEditPage from '@/pages/Apps/ChatApplicationEditPage';
import WorkspacePage from '@/pages/Workspace/WorkspacePage';
import WorkflowsPage from '@/pages/Workflows/WorkflowsPage';
import WorkflowEditorPage from '@/pages/Workflows/WorkflowEditorPage';
import WorkflowRunnerPage from '@/pages/Workflows/WorkflowRunnerPage';
import SkillsPage from '@/pages/Skills/SkillsPage';
import { SchedulesPage } from '@/pages/Schedules/SchedulesPage';

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

/** Console pages scroll and pad; workspaces lay themselves out (§17/§33). */
const consolePage = { shell: { padded: true } };
const fullWidthConsole = { shell: { hideSidebar: true, padded: true } };
const fullscreenConsole = { shell: { hideSidebar: true, hideHeader: true } };

// Mobile shell handles (开发执行报告 §6): the header swaps the workspace
// switcher for a page title; Schedules gains a create action wired by the
// page itself via useMobileHeader. Enterprise sub-pages override the title
// dynamically (mode: 'detail') from EnterprisePage's mobile branch.
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
    hideSidebar: true,
    padded: true,
    mobile: { mode: 'page' as const, title: '定时任务', action: 'create' as const },
  },
};
const enterprisePageHandle = {
  shell: {
    hideSidebar: true,
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
      { path: 'chat/:applicationSlug', element: <WorkspaceHost kind="chat" /> },
      { path: 'app/:applicationSlug', element: <WorkspaceHost kind="page" /> },
      {
        path: 'workflow/:applicationSlug',
        element: <WorkspaceHost kind="workflow" />,
      },

      // ── Console (application management) ──────────────────────────────
      { path: 'agents', element: <AgentsPage />, handle: agentsPageHandle },
      { path: 'agents/:id', element: <AgentDetailPage />, handle: consolePage },
      { path: 'templates', element: <TemplatesPage />, handle: consolePage },
      {
        path: 'templates/:id',
        element: <TemplateDetailPage />,
        handle: consolePage,
      },
      { path: 'apps', element: <AppsPage />, handle: appsPageHandle },
      { path: 'apps/:id', element: <AdminRoute><AppDetailPage /></AdminRoute>, handle: consolePage },
      // Launched apps now live in the shell's application workspace (§32).
      { path: 'apps/:id/run', element: <LegacyAppRunRedirect /> },
      {
        path: 'apps/:id/edit',
        element: <AdminRoute><ChatApplicationEditPage /></AdminRoute>,
        handle: consolePage,
      },
      { path: 'skills', element: <SkillsPage />, handle: fullWidthConsole },
      { path: 'schedules', element: <SchedulesPage />, handle: schedulesPageHandle },
      { path: 'workflows', element: <WorkflowsPage />, handle: fullWidthConsole },
      {
        path: 'workflows/:id/edit',
        element: <WorkflowEditorPage />,
        handle: fullWidthConsole,
      },
      {
        path: 'workflow-runs/:runId',
        element: <WorkflowRunnerPage />,
        handle: fullscreenConsole,
      },
      // Template-workflow workspace: still on the legacy agent engine.
      { path: 'workspace', element: <WorkspacePage />, handle: consolePage },
      {
        path: 'workspace/:id',
        element: <WorkspacePage />,
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
