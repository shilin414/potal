/**
 * MobileProvidersPage — 四态回归（二次复审 P3-1）。
 *
 * loading → Skeleton；error → 错误态 + 重试；success + [] → 暂无
 * Provider；success + data → Provider 卡片。请求失败不得被吞成“暂无数据”。
 */
// @vitest-environment jsdom
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';

const mocks = vi.hoisted(() => ({
  fetchAgentRuntimes: vi.fn(),
}));

vi.mock('@/services/runApi', () => ({
  fetchAgentRuntimes: mocks.fetchAgentRuntimes,
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
  Skeleton: () => <div data-testid="skeleton" />,
}));

import MobileProvidersPage from '../MobileProvidersPage';

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
    root.render(<MobileProvidersPage />);
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

const RUNTIME = {
  key: 'aily',
  provider_key: 'aily',
  provider_name: 'Aily 企业智能体',
  runtime_type: 'aily',
  identity_modes: ['application', 'user'],
  execution_modes: ['sync'],
};

beforeEach(() => {
  mocks.fetchAgentRuntimes.mockReset();
});

describe('MobileProvidersPage — four states (P3-1)', () => {
  it('renders a skeleton while loading', async () => {
    let release: (() => void) | null = null;
    mocks.fetchAgentRuntimes.mockReturnValue(new Promise((resolve) => {
      release = () => resolve([RUNTIME]);
    }));
    await mountPage();
    expect(document.querySelector('[data-testid="skeleton"]')).toBeTruthy();
    await act(async () => { release!(); });
  });

  it('a failure is an error state with retry — never 暂无已注册的 Provider', async () => {
    mocks.fetchAgentRuntimes.mockRejectedValue(new Error('network down'));
    await mountPage();

    expect(document.body.textContent).toContain('加载 Provider 失败');
    expect(document.body.textContent).not.toContain('暂无已注册的 Provider');
    const retry = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '重试');
    expect(retry).toBeTruthy();

    mocks.fetchAgentRuntimes.mockResolvedValue([RUNTIME]);
    await click(retry!);
    await flush(20);
    expect(document.body.textContent).toContain('Aily 企业智能体');
  });

  it('a successful empty list renders the empty state', async () => {
    mocks.fetchAgentRuntimes.mockResolvedValue([]);
    await mountPage();
    expect(document.body.textContent).toContain('暂无已注册的 Provider');
  });

  it('a successful list renders provider cards', async () => {
    mocks.fetchAgentRuntimes.mockResolvedValue([RUNTIME]);
    await mountPage();

    expect(document.querySelector('.mobile-provider-card__name')!.textContent)
      .toBe('Aily 企业智能体');
    const card = document.querySelector('.mobile-provider-card')!;
    expect(card.textContent).toContain('aily');
    expect(card.textContent).toContain('application / user');
    expect(card.textContent).toContain('sync');
  });
});
