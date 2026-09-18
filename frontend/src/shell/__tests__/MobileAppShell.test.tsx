// @vitest-environment jsdom

import React, { act, useMemo, useState } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import {
  MemoryRouter,
  Route,
  Routes,
  useLocation,
} from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('antd', () => ({
  Button: ({ children, icon, onClick, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement> & { icon?: React.ReactNode }) => (
    <button type="button" onClick={onClick} {...props}>{icon}{children}</button>
  ),
  Drawer: ({ open, children, title, onClose }: {
    open: boolean;
    children: React.ReactNode;
    title?: React.ReactNode;
    onClose: () => void;
  }) => open ? (
    <aside data-testid="drawer">
      <div>{title}</div>
      <button type="button" onClick={onClose}>关闭抽屉</button>
      {children}
    </aside>
  ) : null,
}));
vi.mock('@/components/ConversationHistory/ConversationHistory', () => ({
  default: ({ onNewConversation }: { onNewConversation: () => void }) => (
    <button type="button" onClick={onNewConversation}>最近会话</button>
  ),
}));
vi.mock('@/components/AccountMenu/AccountMenu', () => ({
  default: () => <div>账号</div>,
}));
vi.mock('@/components/Mobile/MobileAgentSwitcher', () => ({
  default: () => <div>智能体切换</div>,
}));
vi.mock('@/components/Theme', () => ({
  ThemePicker: () => <div>主题</div>,
}));

import MobileAppShell from '@/shell/MobileAppShell';
import { useMobileHeader } from '@/shell/mobileHeader';
import { useAuthStore } from '@/stores/useAuthStore';
import { useNavigationPreferencesStore } from '@/stores/useNavigationPreferencesStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Array<{ host: HTMLElement; root: Root }> = [];

const LocationProbe = () => {
  const location = useLocation();
  return <output data-testid="location">{location.pathname}</output>;
};

function buttonByText(text: string): HTMLButtonElement {
  const button = Array.from(document.querySelectorAll<HTMLButtonElement>('button'))
    .find((item) => item.textContent?.includes(text));
  if (!button) throw new Error(`missing button: ${text}`);
  return button;
}

async function mountShell(initialPath: string) {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  roots.push({ host, root });
  await act(async () => {
    root.render(
      <MemoryRouter initialEntries={[initialPath]}>
        <Routes>
          <Route
            path="*"
            element={(
              <>
                <MobileAppShell chrome={{ hideHeader: false, hideSidebar: false, padded: false }} />
                <LocationProbe />
              </>
            )}
          />
        </Routes>
      </MemoryRouter>,
    );
  });
}

beforeEach(() => {
  useWorkspaceStore.setState({ mobileNavOpen: false });
  useNavigationPreferencesStore.setState({ iconMode: 'outline', icons: {} });
  useAuthStore.setState({
    user: {
      id: '1', username: 'tester', email: '', role: 'user',
      display_name: '测试用户', created_at: '', is_staff: true,
    },
    isAuthenticated: true,
  });
});

afterEach(async () => {
  while (roots.length) {
    const { host, root } = roots.pop()!;
    await act(async () => root.unmount());
    host.remove();
  }
  document.body.innerHTML = '';
});

describe('MobileAppShell navigation regression', () => {
  it('opens the drawer, keeps supporting surfaces, marks chat active, and closes after navigation', async () => {
    await mountShell('/chat/main-agent');

    const menuButton = document.querySelector<HTMLButtonElement>('[aria-label="打开导航"]');
    if (!menuButton) throw new Error('missing mobile menu button');
    await act(async () => menuButton.click());
    const drawer = document.querySelector('[data-testid="drawer"]');
    expect(drawer).toBeTruthy();
    expect(drawer?.textContent).toContain('最近会话');
    expect(drawer?.textContent).toContain('主题');
    expect(drawer?.textContent).toContain('账号');

    const home = buttonByText('首页');
    expect(home.getAttribute('aria-current')).toBe('page');
    expect(home.querySelector('.mobile-shell__nav-icon')).toBeTruthy();

    await act(async () => buttonByText('应用').click());
    expect(document.querySelector('[data-testid="drawer"]')).toBeNull();
    expect(document.querySelector('[data-testid="location"]')?.textContent).toBe('/apps');
  });

  it('keeps 新任务 navigating to the mobile home route', async () => {
    await mountShell('/agents');
    await act(async () => buttonByText('新任务').click());
    expect(document.querySelector('[data-testid="location"]')?.textContent).toBe('/');
  });
});

// ── Route-aware header (开发执行报告 §5/§78) ───────────────────────────

/** A page that binds the shell's trailing action, like MobileScheduleCenter. */
const HeaderActionProbe = () => {
  const [count, setCount] = useState(0);
  const options = useMemo(() => ({ onAction: () => setCount((c) => c + 1) }), []);
  useMobileHeader(options);
  return <output data-testid="probe">{count}</output>;
};

/** A sub-page that overrides the header, like MobileEnterpriseConsole. */
const DetailProbe = () => {
  const options = useMemo(() => ({
    mode: 'detail' as const,
    title: '智能体管理',
    backTo: '/enterprise',
  }), []);
  useMobileHeader(options);
  return <LocationProbe />;
};

/**
 * The enterprise resource page scenario: the console shell registers a
 * detail layer (← 智能体管理) while the page inside it registers the create
 * action. The two layers must MERGE, not clobber (审查 MAJOR 回归).
 */
const EnterpriseResourceProbe = () => {
  const detail = useMemo(() => ({
    mode: 'detail' as const,
    title: '智能体管理',
    backTo: '/enterprise',
  }), []);
  useMobileHeader(detail);

  // Mirrors MobileResourcePage: the page itself declares the create action.
  const [count, setCount] = useState(0);
  const create = useMemo(() => ({
    action: 'create' as const,
    onAction: () => setCount((c) => c + 1),
  }), []);
  useMobileHeader(create);
  return <output data-testid="probe">{count}</output>;
};

async function mountShellWithChrome(
  chrome: Record<string, unknown>,
  child: React.ReactElement = <LocationProbe />,
  initialPath = '/schedules',
) {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  roots.push({ host, root });
  await act(async () => {
    root.render(
      <MemoryRouter initialEntries={[initialPath]}>
        <Routes>
          <Route path="*" element={<MobileAppShell chrome={chrome as never} />}>
            <Route path="*" element={child} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
  });
}

describe('MobileAppShell route-aware header', () => {
  it('page mode swaps the switcher for the page title (§5 智能体中心)', async () => {
    await mountShellWithChrome({
      hideHeader: false, hideSidebar: false, padded: true,
      mobile: { mode: 'page', title: '智能体中心' },
    }, undefined, '/agents');

    expect(document.querySelector('.mobile-shell__title')?.textContent)
      .toBe('智能体中心');
    expect(document.body.textContent).not.toContain('智能体切换');
    expect(document.body.textContent).not.toContain('新任务');
    // ☰ still opens the drawer.
    expect(document.querySelector('[aria-label="打开导航"]')).toBeTruthy();
  });

  it('detail mode backs to the console root instead of history (§5 企业二级页面)', async () => {
    await mountShellWithChrome({
      hideHeader: false, hideSidebar: false, padded: true,
      mobile: { mode: 'console', title: '企业控制台' },
    }, <DetailProbe />, '/enterprise/resources/agents');

    expect(document.querySelector('.mobile-shell__title')?.textContent)
      .toBe('智能体管理');
    const back = document.querySelector<HTMLButtonElement>('[aria-label="返回企业控制台"]');
    expect(back).toBeTruthy();
    await act(async () => back!.click());
    expect(document.querySelector('[data-testid="location"]')?.textContent)
      .toBe('/enterprise');
  });

  it('schedules create action fires the page-bound onAction (§5 定时任务 ＋)', async () => {
    await mountShellWithChrome({
      hideHeader: false, hideSidebar: false, padded: true,
      mobile: { mode: 'page', title: '定时任务', action: 'create' },
    }, <HeaderActionProbe />, '/schedules');

    const create = document.querySelector<HTMLButtonElement>('[aria-label="新建"]');
    expect(create).toBeTruthy();
    await act(async () => create!.click());
    expect(document.querySelector('[data-testid="probe"]')?.textContent).toBe('1');
  });

  it('keeps the create action disabled until a page binds onAction', async () => {
    await mountShellWithChrome({
      hideHeader: false, hideSidebar: false, padded: true,
      mobile: { mode: 'page', title: '定时任务', action: 'create' },
    }, undefined, '/schedules');

    const create = document.querySelector<HTMLButtonElement>('[aria-label="新建"]');
    expect(create?.disabled).toBe(true);
  });

  it('merges nested header layers instead of clobbering (企业资源页 ＋)', async () => {
    await mountShellWithChrome({
      hideHeader: false, hideSidebar: false, padded: true,
      mobile: { mode: 'console', title: '企业控制台' },
    }, <EnterpriseResourceProbe />, '/enterprise/resources/agents');

    // Detail layer from the console shell…
    expect(document.querySelector('.mobile-shell__title')?.textContent)
      .toBe('智能体管理');
    expect(document.querySelector('[aria-label="返回企业控制台"]')).toBeTruthy();
    // …AND the create action from the page inside it.
    const create = document.querySelector<HTMLButtonElement>('[aria-label="新建"]');
    expect(create).toBeTruthy();
    expect(create?.disabled).toBe(false);
    await act(async () => create!.click());
    expect(document.querySelector('[data-testid="probe"]')?.textContent).toBe('1');
  });

  it('hides 企业控制台 from the drawer for non-staff users (§78)', async () => {
    useAuthStore.setState({
      user: {
        id: '2', username: 'user', email: '', role: 'user',
        display_name: '普通用户', created_at: '', is_staff: false,
      },
      isAuthenticated: true,
    });
    await mountShell('/');

    await act(async () => {
      document.querySelector<HTMLButtonElement>('[aria-label="打开导航"]')!.click();
    });
    const drawer = document.querySelector('[data-testid="drawer"]');
    expect(drawer?.textContent).not.toContain('企业控制台');
  });
});
