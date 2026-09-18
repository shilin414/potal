/**
 * MobileAuditPage — 状态与详情 Sheet 回归（二次复审 P3-1）。
 *
 * loading → Skeleton；error → 错误态 + 重试；success + [] → 暂无审计
 * 日志；success + data → Timeline 行；点击行 → Bottom Sheet 显示 JSON
 * detail。请求失败不得被吞成“暂无审计日志”。
 */
// @vitest-environment jsdom
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';

const mocks = vi.hoisted(() => ({
  audits: vi.fn(),
}));

vi.mock('../../enterpriseApi', () => ({
  enterpriseApi: {
    audits: mocks.audits,
  },
}));

vi.mock('../../enterpriseNav', () => ({
  fmt: (value: string | null | undefined) => (value ?? '—').slice(0, 10),
}));

vi.mock('@/components/MobileConsole', () => ({
  MobileEmptyState: ({ title, action }: {
    title?: React.ReactNode;
    action?: React.ReactNode;
  }) => (
    <div data-testid="empty-state"><span>{title}</span>{action}</div>
  ),
  MobilePage: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
  MobileSection: ({ title, children }: { title?: string; children?: React.ReactNode }) => (
    <section data-testid="section"><h3>{title}</h3>{children}</section>
  ),
}));

vi.mock('antd', () => ({
  Button: ({ children, onClick }: React.ButtonHTMLAttributes<HTMLButtonElement>) => (
    <button type="button" onClick={onClick}>{children}</button>
  ),
  Drawer: ({ open, children }: { open?: boolean; children?: React.ReactNode }) => (
    open ? <div data-testid="drawer">{children}</div> : null
  ),
  Skeleton: () => <div data-testid="skeleton" />,
}));

import MobileAuditPage from '../MobileAuditPage';

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
async function mountPage() {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  mounted.push({ host, root });
  await act(async () => {
    root.render(<MobileAuditPage />);
  });
  await flush(20);
  return { host, root };
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

const LOG = (id: number, action: string) => ({
  id,
  user_id: id,
  action,
  resource_type: 'application',
  resource_id: String(id),
  detail: { application_id: id, note: `audit-${id}` },
  created_at: '2026-09-18T02:00:00Z',
});

beforeEach(() => {
  mocks.audits.mockReset();
});

describe('MobileAuditPage — states (P3-1)', () => {
  it('renders a skeleton while loading', async () => {
    let release: (() => void) | null = null;
    mocks.audits.mockReturnValue(new Promise((resolve) => {
      release = () => resolve([LOG(1, 'access.update')]);
    }));
    await mountPage();
    expect(document.querySelector('[data-testid="skeleton"]')).toBeTruthy();
    await act(async () => { release!(); });
  });

  it('a failure is an error state with retry — never 暂无审计日志', async () => {
    mocks.audits.mockRejectedValue(new Error('network down'));
    await mountPage();

    expect(document.body.textContent).toContain('加载审计日志失败');
    expect(document.body.textContent).not.toContain('暂无审计日志');
    const retry = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '重试');
    expect(retry).toBeTruthy();

    mocks.audits.mockResolvedValue([LOG(1, 'access.update')]);
    await click(retry!);
    await flush(20);
    expect(document.body.textContent).toContain('access.update');
  });

  it('a successful empty list renders the empty state', async () => {
    mocks.audits.mockResolvedValue([]);
    await mountPage();
    expect(document.body.textContent).toContain('暂无审计日志');
  });

  it('a successful list renders rows and a row tap opens the detail sheet', async () => {
    mocks.audits.mockResolvedValue([LOG(1, 'access.update'), LOG(2, 'directory.sync')]);
    await mountPage();

    const rows = Array.from(document.querySelectorAll('.mobile-audit__item'));
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain('access.update');
    expect(rows[0].textContent).toContain('application:1');
    expect(rows[0].textContent).toContain('操作者：1');

    await click(rows[0]);
    await flush(20);
    const sheet = document.querySelector('[data-testid="drawer"]')!;
    expect(sheet.textContent).toContain('access.update · application:1');
    expect(sheet.textContent).toContain('audit-1');
  });
});
