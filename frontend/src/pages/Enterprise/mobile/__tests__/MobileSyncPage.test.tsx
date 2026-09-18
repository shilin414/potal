/**
 * MobileSyncPage — 配置写入守卫与失败可见性回归（二次复审 P1-3/P2-7/P2-8）。
 *
 *   · 配置未加载成功 → 绝不存在“保存设置”按钮（前端默认值不得写回服务器）；
 *   · load 失败 → 显式错误态 + 重试（保留旧数据，不再 unhandled rejection）;
 *   · 已加载后的刷新失败 → stale warning 可见 + 旧数据保留；
 *   · config 与 runs 独立失败域：syncRuns 挂了不影响配置表单；
 *   · save 失败 → message.error('保存同步设置失败')；
 *   · trigger 失败 → message.error('发起同步失败')。
 */
// @vitest-environment jsdom
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';

const mocks = vi.hoisted(() => ({
  syncConfig: vi.fn(),
  syncRuns: vi.fn(),
  updateSyncConfig: vi.fn(),
  triggerSync: vi.fn(),
  messageError: vi.fn(),
  messageSuccess: vi.fn(),
}));

vi.mock('../../enterpriseApi', () => ({
  enterpriseApi: {
    syncConfig: mocks.syncConfig,
    syncRuns: mocks.syncRuns,
    updateSyncConfig: mocks.updateSyncConfig,
    triggerSync: mocks.triggerSync,
  },
}));

vi.mock('antd', () => {
  const FormMock = ({ children }: { children?: React.ReactNode }) => <form>{children}</form>;
  // Form.Item must also support the render-function form used by
  // `<Form.Item noStyle shouldUpdate>{({ getFieldValue }) => …}</Form.Item>`.
  const FormItemMock = ({ label, children }: {
    label?: React.ReactNode;
    children?: React.ReactNode | ((api: { getFieldValue: (name: string) => unknown }) => React.ReactNode);
  }) => {
    const content = typeof children === 'function'
      ? children({ getFieldValue: () => 'interval' })
      : children;
    return <label>{label}{content}</label>;
  };
  (FormMock as unknown as { Item: unknown }).Item = FormItemMock;
  // ONE stable form instance — the real Form.useForm() keeps its reference
  // across renders, and MobileSyncPage's `useCallback(load, [form])` +
  // `useEffect([load])` would loop forever on a fresh object per render.
  const formApi = {
    setFieldsValue: vi.fn(),
    validateFields: vi.fn(async () => ({ daily_time: { format: () => '02:00' } })),
  };
  return {
    Alert: ({ message, action }: {
      message?: React.ReactNode;
      action?: React.ReactNode;
    }) => (
      <div role="alert">{message}{action}</div>
    ),
    Button: ({ children, onClick, className }: React.ButtonHTMLAttributes<HTMLButtonElement> & { className?: string }) => (
      <button type="button" className={className} onClick={onClick}>{children}</button>
    ),
    Form: Object.assign(FormMock, { useForm: () => [formApi] }),
    Input: () => <input />,
    InputNumber: () => <input type="number" />,
    Select: () => <select />,
    Skeleton: () => <div data-testid="skeleton" />,
    Switch: () => <button type="button" data-testid="switch" />,
    Tag: ({ children }: { children?: React.ReactNode }) => <span>{children}</span>,
    TimePicker: () => <input type="time" />,
    message: { success: mocks.messageSuccess, error: mocks.messageError },
  };
});

vi.mock('@/components/MobileConsole', () => ({
  MobilePage: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
  MobileSection: ({ title, children }: { title?: string; children?: React.ReactNode }) => (
    <section data-testid="section"><h3>{title}</h3>{children}</section>
  ),
}));

import MobileSyncPage from '../MobileSyncPage';

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
    root.render(<MobileSyncPage />);
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

const CFG = {
  enabled: false,
  schedule_type: 'interval',
  interval_minutes: 360,
  daily_time: '02:00',
  timezone: 'Asia/Shanghai',
  next_run_at: null,
  last_run_at: null,
  last_success_at: '2026-09-18T02:00:00Z',
  updated_at: '2026-09-18T02:00:00Z',
};

const RUN = (id: number, status: string) => ({
  id, trigger_type: 'scheduled', status,
  departments_count: 1, users_count: 2, active_users_count: 2,
  memberships_count: 3, active_memberships_count: 3,
  started_at: null, finished_at: null,
  error_code: '', error_message: '', created_at: '2026-09-18T02:00:00Z',
});

beforeEach(() => {
  mocks.syncConfig.mockReset().mockResolvedValue(CFG);
  mocks.syncRuns.mockReset().mockResolvedValue([RUN(1, 'success')]);
  mocks.updateSyncConfig.mockReset();
  mocks.triggerSync.mockReset();
  mocks.messageError.mockReset();
  mocks.messageSuccess.mockReset();
});

describe('MobileSyncPage — failure visibility (P2-8)', () => {
  it('a load failure renders an explicit error state with retry', async () => {
    mocks.syncConfig.mockRejectedValue(new Error('network down'));
    mocks.syncRuns.mockRejectedValue(new Error('network down'));
    await mountPage();

    expect(document.body.textContent).toContain('加载失败');
    const retry = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '重试');
    expect(retry).toBeTruthy();

    mocks.syncConfig.mockResolvedValue(CFG);
    mocks.syncRuns.mockResolvedValue([RUN(1, 'success')]);
    await click(retry!);
    await flush(20);
    expect(document.body.textContent).toContain('最近同步');
  });

  it('a save failure surfaces 保存同步设置失败', async () => {
    await mountPage();
    mocks.updateSyncConfig.mockRejectedValue(new Error('server down'));

    const save = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '保存设置');
    expect(save).toBeTruthy();
    await click(save!);
    await flush(20);

    expect(mocks.messageError).toHaveBeenCalledWith('保存同步设置失败');
  });

  it('a trigger failure surfaces 发起同步失败', async () => {
    await mountPage();
    mocks.triggerSync.mockRejectedValue(new Error('server down'));

    const trigger = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '立即同步');
    expect(trigger).toBeTruthy();
    await click(trigger!);
    await flush(20);

    expect(mocks.messageError).toHaveBeenCalledWith('发起同步失败');
  });
});

describe('MobileSyncPage — config write guard (P1-3)', () => {
  it('an initial config load failure renders NO 保存设置 button — defaults must not be savable', async () => {
    mocks.syncConfig.mockRejectedValue(new Error('network down'));
    mocks.syncRuns.mockResolvedValue([RUN(1, 'success')]);
    await mountPage();

    expect(document.body.textContent).toContain('无法加载同步配置');
    const save = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '保存设置');
    expect(save).toBeUndefined();
  });

  it('a failed refresh AFTER a successful load shows the stale-data warning and keeps the form', async () => {
    await mountPage();
    // Loaded once (CFG). Now a trigger succeeds but the follow-up config
    // refresh fails — the old cfg must survive with a visible warning.
    mocks.triggerSync.mockResolvedValue(RUN(2, 'pending'));
    mocks.syncConfig.mockRejectedValue(new Error('network down'));

    const trigger = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '立即同步');
    await click(trigger!);
    await flush(20);

    expect(document.body.textContent).toContain('刷新失败，当前显示的是上次已加载数据');
    // The stale form is still there (loaded config, not defaults).
    const save = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '保存设置');
    expect(save).toBeTruthy();
  });

  it('syncRuns failing alone must not take down the config form (P2-8 independent failure domains)', async () => {
    mocks.syncRuns.mockRejectedValue(new Error('network down'));
    await mountPage();

    const save = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '保存设置');
    expect(save).toBeTruthy();
    expect(document.body.textContent).toContain('加载同步记录失败');
  });
});
