/**
 * MobileEnterpriseHome — 概览失败降级回归（二次复审 P2-9）。
 *
 * The overview is the ONLY thing that fails: the navigation menu must stay
 * usable and a retry must exist. A stats/syncRuns rejection must not leave a
 * silently blank card grid forever.
 */
// @vitest-environment jsdom
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';

const mocks = vi.hoisted(() => ({
  stats: vi.fn(),
  syncRuns: vi.fn(),
  navigate: vi.fn(),
}));

vi.mock('../../enterpriseApi', () => ({
  enterpriseApi: {
    stats: mocks.stats,
    syncRuns: mocks.syncRuns,
  },
}));

vi.mock('../../enterpriseNav', () => ({
  ENTERPRISE_SECTIONS: [
    {
      title: '资源',
      items: [{ key: 'resources/agents', label: '智能体管理', icon: null }],
    },
  ],
  pagePath: (key: string) => `/enterprise/${key}`,
}));

vi.mock('react-router-dom', () => ({
  useNavigate: () => mocks.navigate,
}));

vi.mock('antd', () => ({
  Alert: ({ message, action }: { message?: React.ReactNode; action?: React.ReactNode }) => (
    <div role="alert">{message}{action}</div>
  ),
  Button: ({ children, onClick }: React.ButtonHTMLAttributes<HTMLButtonElement>) => (
    <button type="button" onClick={onClick}>{children}</button>
  ),
}));

vi.mock('@/components/MobileConsole', () => ({
  MobilePage: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
  MobileSection: ({ title, children }: { title?: string; children?: React.ReactNode }) => (
    <section data-testid="section"><h3>{title}</h3>{children}</section>
  ),
  MobileSettingsGroup: ({ title, children }: { title?: string; children?: React.ReactNode }) => (
    <div data-testid="settings-group">{title}{children}</div>
  ),
  MobileSettingsRow: ({ title, onClick }: { title: string; onClick?: () => void }) => (
    <button type="button" className="mobile-console-settings-row" onClick={onClick}>
      {title}
    </button>
  ),
  MobileStatCard: ({ value, label }: { value?: string; label?: string }) => (
    <div data-testid="stat-card" data-label={label}>{value}</div>
  ),
}));

import MobileEnterpriseHome from '../MobileEnterpriseHome';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
};
(globalThis as unknown as { matchMedia: unknown }).matchMedia = (query: string) => ({
  matches: false, media: query, onchange: null,
  addListener() {}, removeListener() {},
  addEventListener() {}, removeEventListener() {},
  dispatchEvent: () => false,
});

const flush = async (ms = 0) => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms));
  });
};

const mounted: Array<{ host: HTMLElement; root: Root }> = [];
async function mountHome() {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  mounted.push({ host, root });
  await act(async () => {
    root.render(<MobileEnterpriseHome />);
  });
  await flush(20);
}

function click(el: Element) {
  return act(async () => {
    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

afterEach(async () => {
  while (mounted.length) {
    const { host, root } = mounted.pop()!;
    await act(async () => { root.unmount(); });
    host.remove();
  }
  document.body.innerHTML = '';
});

const STATS = {
  departments_total: 5,
  departments_active: 4,
  users_total: 120,
  users_active: 100,
  users_resigned: 3,
  oauth_users: 60,
  linked_directory_users: 30,
};

beforeEach(() => {
  mocks.stats.mockReset().mockResolvedValue(STATS);
  mocks.syncRuns.mockReset().mockResolvedValue([{
    id: 1, trigger_type: 'scheduled', status: 'success',
    departments_count: 1, users_count: 2, active_users_count: 2,
    memberships_count: 3, active_memberships_count: 3,
    started_at: null, finished_at: null,
    error_code: '', error_message: '', created_at: '2026-09-18T02:00:00Z',
  }]);
  mocks.navigate.mockReset();
});

describe('MobileEnterpriseHome (P2-9)', () => {
  it('renders stat cards and the navigation menu on success', async () => {
    await mountHome();

    const stats = Array.from(document.querySelectorAll('[data-testid="stat-card"]'));
    expect(stats.length).toBe(4);
    const labels = stats.map((el) => el.getAttribute('data-label'));
    expect(labels).toContain('有效部门');
    expect(labels).toContain('有效员工');
    expect(document.querySelector('.mobile-console-settings-row')!.textContent)
      .toContain('智能体管理');
  });

  it('an overview failure keeps the menu usable and offers a retry', async () => {
    mocks.stats.mockRejectedValue(new Error('network down'));
    mocks.syncRuns.mockRejectedValue(new Error('network down'));
    await mountHome();

    expect(document.body.textContent).toContain('企业概览暂时无法加载');
    // The menu is still there and still navigates.
    const menuRow = document.querySelector<HTMLButtonElement>('.mobile-console-settings-row')!;
    expect(menuRow.textContent).toContain('智能体管理');
    await click(menuRow);
    expect(mocks.navigate).toHaveBeenCalledWith('/enterprise/resources/agents');

    // Retry recovers the overview.
    mocks.stats.mockResolvedValue(STATS);
    mocks.syncRuns.mockResolvedValue([]);
    const retry = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '重试');
    expect(retry).toBeTruthy();
    await click(retry!);
    await flush(20);
    expect(Array.from(document.querySelectorAll('[data-testid="stat-card"]')).length)
      .toBe(4);
  });
});
