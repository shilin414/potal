import { lazy, Suspense } from 'react';
import { createBrowserRouter, Navigate } from 'react-router-dom';
import { AuthLayout } from '@/layouts';
import AppShell from '@/shell/AppShell';
import WorkspaceHost from '@/components/Workspace/WorkspaceHost';
import { ProtectedRoute, PublicRoute } from './guards';
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

// Auth pages stay outside the shell entirely.
import LoginPage from '@/pages/Auth/LoginPage';
import RegisterPage from '@/pages/Auth/RegisterPage';
import SsoCallbackPage from '@/pages/Auth/SsoCallbackPage';
import FeishuCallbackPage from '@/pages/Auth/FeishuCallbackPage';

const EnterprisePage = lazy(() => import('@/pages/Enterprise/EnterprisePage'));

const enterpriseElement = (
  <Suspense fallback={<div style={{ padding: 32 }}>正在加载企业控制台…</div>}>
    <EnterprisePage />
  </Suspense>
);

/** Console pages scroll and pad; workspaces lay themselves out (§17/§33). */
const consolePage = { shell: { padded: true } };
const fullWidthConsole = { shell: { hideSidebar: true, padded: true } };
const fullscreenConsole = { shell: { hideSidebar: true, hideHeader: true } };

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
      { path: 'agents', element: <AgentsPage />, handle: consolePage },
      { path: 'agents/:id', element: <AgentDetailPage />, handle: consolePage },
      { path: 'templates', element: <TemplatesPage />, handle: consolePage },
      {
        path: 'templates/:id',
        element: <TemplateDetailPage />,
        handle: consolePage,
      },
      { path: 'apps', element: <AppsPage />, handle: consolePage },
      { path: 'apps/:id', element: <AppDetailPage />, handle: consolePage },
      // Launched apps now live in the shell's application workspace (§32).
      { path: 'apps/:id/run', element: <LegacyAppRunRedirect /> },
      {
        path: 'apps/:id/edit',
        element: <ChatApplicationEditPage />,
        handle: consolePage,
      },
      { path: 'skills', element: <SkillsPage />, handle: fullWidthConsole },
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
      { path: 'enterprise', element: enterpriseElement, handle: consolePage },

      { path: '*', element: <Navigate to="/" replace /> },
    ],
  },
  // ── Auth (no shell) ───────────────────────────────────────────────────
  {
    path: '/auth/sso/callback',
    element: <AuthLayout><SsoCallbackPage /></AuthLayout>,
  },
  {
    path: '/auth/feishu/callback',
    element: <AuthLayout><FeishuCallbackPage /></AuthLayout>,
  },
  {
    path: '/auth/login',
    element: <AuthLayout><PublicRoute><LoginPage /></PublicRoute></AuthLayout>,
  },
  {
    path: '/auth/register',
    element: <AuthLayout><PublicRoute><RegisterPage /></PublicRoute></AuthLayout>,
  },
  { path: '*', element: <Navigate to="/" replace /> },
]);

export default router;
