/**
 * MobileScheduleCenter — 移动定时任务中心行为回归（开发执行报告 §79）。
 *
 * 验证：卡片信息层次、••• ActionSheet 收纳全部二级操作、
 * 立即运行 / 编辑 / 查看执行记录 触发正确动作，且不改变请求语义
 * （同一 scheduleApi，payload 不受 UI 重构影响）。
 */
// @vitest-environment jsdom
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';

const mocks = vi.hoisted(() => ({
  fetchSchedules: vi.fn(),
  enableSchedule: vi.fn(),
  disableSchedule: vi.fn(),
  runScheduleNow: vi.fn(),
  deleteSchedule: vi.fn(),
  modalConfirm: vi.fn(),
}));

vi.mock('antd', () => {
  const ModalMock: React.FC<{ open?: boolean; children?: React.ReactNode }>
    = ({ open, children }) => (open
      ? <aside data-testid="modal">{children}</aside> : null);
  (ModalMock as unknown as { confirm: unknown }).confirm = mocks.modalConfirm;
  return {
    Alert: ({
      message, description, action, children,
    }: {
      message?: React.ReactNode;
      description?: React.ReactNode;
      action?: React.ReactNode;
      children?: React.ReactNode;
    }) => (
      <div role="alert">
        {message}
        {description}
        {action}
        {children}
      </div>
    ),
    Button: ({ children, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement>) => (
      <button type="button" {...props}>{children}</button>
    ),
    Drawer: ({ open, children }: { open: boolean; children?: React.ReactNode }) => (
      open ? <aside data-testid="drawer">{children}</aside> : null
    ),
    Input: (props: React.InputHTMLAttributes<HTMLInputElement>) => (
      <input {...props} />
    ),
    Modal: ModalMock,
    Segmented: ({ value, onChange, options }: {
      value: string;
      onChange: (v: string) => void;
      options: Array<{ value: string; label: string }>;
    }) => (
      <div data-testid="segmented">
        {options.map((o) => (
          <button
            key={o.value}
            type="button"
            data-value={o.value}
            className={o.value === value ? 'active' : ''}
            onClick={() => onChange(o.value)}
          >
            {o.label}
          </button>
        ))}
      </div>
    ),
    Skeleton: () => <div data-testid="skeleton" />,
    Tag: ({ children }: { children?: React.ReactNode }) => (
      <span data-testid="tag">{children}</span>
    ),
    message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  };
});

vi.mock('@/services/scheduleApi', () => ({
  fetchSchedules: mocks.fetchSchedules,
  enableSchedule: mocks.enableSchedule,
  disableSchedule: mocks.disableSchedule,
  runScheduleNow: mocks.runScheduleNow,
  deleteSchedule: mocks.deleteSchedule,
}));

vi.mock('@/components/Schedules/MobileScheduleEditor', () => ({
  MobileScheduleEditor: ({ open, editing }: { open: boolean; editing: { name: string } | null }) => (
    open ? <div data-testid="mobile-editor">{editing ? `编辑:${editing.name}` : '新建'}</div> : null
  ),
}));
vi.mock('@/components/Schedules/MobileScheduleDetail', () => ({
  MobileScheduleDetail: ({ open, scheduleId }: { open: boolean; scheduleId: number | null }) => (
    open ? <div data-testid="mobile-detail">{scheduleId}</div> : null
  ),
}));

import { MobileScheduleCenter } from '../MobileScheduleCenter';
import type { Schedule } from '@/types/schedule';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const schedule = (over: Partial<Schedule> & { id: number }): Schedule => ({
  name: `任务${over.id}`,
  description: '',
  application_id: 7,
  prompt: '总结今天的数据',
  schedule_type: 'daily',
  cron_expression: '0 0 9 * * *',
  timezone: 'Asia/Shanghai',
  run_at: null,
  trigger: { time: '09:00', days_of_week: [1, 2, 3, 4, 5], day_of_month: 1 },
  enabled: true,
  conversation_policy: 'new_each_run',
  overlap_policy: 'queue',
  misfire_policy: 'fire_once',
  deadline_policy: 'execute_anyway',
  execution_window_seconds: 0,
  next_run_at: '2026-09-19T01:00:00Z',
  last_run_at: null,
  created_at: '2026-09-01T00:00:00Z',
  updated_at: '2026-09-01T00:00:00Z',
  deliveries: [],
  ...over,
});

const SCHEDULES = [
  schedule({ id: 1, name: '每日销售日报' }),
  schedule({ id: 2, name: '每周周报', enabled: false }),
];

const mounted: Array<{ host: HTMLElement; root: Root }> = [];

async function mountCenter() {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  mounted.push({ host, root });
  await act(async () => {
    root.render(<MobileScheduleCenter />);
  });
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 20));
  });
}

const flush = async (ms = 0) => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms));
  });
};

function click(el: Element) {
  return act(async () => {
    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

const actionRow = (label: string) => (
  Array.from(document.querySelectorAll<HTMLElement>('.mobile-action-sheet__row'))
    .find((el) => el.textContent?.includes(label)));

beforeEach(() => {
  mocks.fetchSchedules.mockReset().mockResolvedValue(SCHEDULES);
  mocks.runScheduleNow.mockReset().mockResolvedValue({ id: 1 });
  mocks.enableSchedule.mockReset().mockResolvedValue(schedule({ id: 1 }));
  mocks.disableSchedule.mockReset().mockResolvedValue(schedule({ id: 1, enabled: false }));
  mocks.deleteSchedule.mockReset().mockResolvedValue({ id: 1 });
  mocks.modalConfirm.mockReset();
});

afterEach(async () => {
  while (mounted.length) {
    const { host, root } = mounted.pop()!;
    await act(async () => { root.unmount(); });
    host.remove();
  }
  document.body.innerHTML = '';
});

describe('MobileScheduleCenter (§25/§27/§79)', () => {
  it('renders cards with compact status, plan and next run — no desktop table', async () => {
    await mountCenter();

    const cards = document.querySelectorAll('.mobile-schedule-card');
    expect(cards).toHaveLength(2);
    expect(cards[0].textContent).toContain('每日销售日报');
    expect(cards[0].textContent).toContain('下次执行');
    expect(cards[0].textContent).toContain('运行中');
    expect(cards[1].textContent).toContain('已暂停');
    expect(document.querySelector('.ant-table')).toBeNull();
  });

  it('••• collects the secondary actions into the ActionSheet (§27)', async () => {
    await mountCenter();
    expect(document.querySelector('.mobile-action-sheet__row')).toBeNull();

    await click(document.querySelector('[aria-label="更多操作：每日销售日报"]')!);
    for (const label of ['立即运行', '编辑', '暂停', '查看执行记录', '删除']) {
      expect(actionRow(label)).toBeTruthy();
    }
  });

  it('立即运行 calls runScheduleNow with the same id semantics', async () => {
    await mountCenter();
    await click(document.querySelector('[aria-label="更多操作：每日销售日报"]')!);
    await click(actionRow('立即运行')!);
    expect(mocks.runScheduleNow).toHaveBeenCalledWith(1);
  });

  it('编辑 opens the full-screen editor bound to the schedule', async () => {
    await mountCenter();
    await click(document.querySelector('[aria-label="更多操作：每日销售日报"]')!);
    await click(actionRow('编辑')!);
    expect(document.querySelector('[data-testid="mobile-editor"]')?.textContent)
      .toBe('编辑:每日销售日报');
  });

  it('查看执行记录 opens the full-screen detail', async () => {
    await mountCenter();
    await click(document.querySelector('[aria-label="更多操作：每日销售日报"]')!);
    await click(actionRow('查看执行记录')!);
    expect(document.querySelector('[data-testid="mobile-detail"]')?.textContent)
      .toBe('1');
  });

  it('删除 routes through Modal.confirm, never a bare delete (§27)', async () => {
    await mountCenter();
    await click(document.querySelector('[aria-label="更多操作：每日销售日报"]')!);
    await click(actionRow('删除')!);
    expect(mocks.modalConfirm).toHaveBeenCalledTimes(1);
    expect(mocks.deleteSchedule).not.toHaveBeenCalled();
    const options = mocks.modalConfirm.mock.calls[0][0] as { onOk: () => Promise<void> };
    await act(async () => { await options.onOk(); });
    expect(mocks.deleteSchedule).toHaveBeenCalledWith(1);
  });

  it('暂停 toggles through the shared useSchedules API', async () => {
    await mountCenter();
    await click(document.querySelector('[aria-label="更多操作：每日销售日报"]')!);
    await click(actionRow('暂停')!);
    expect(mocks.disableSchedule).toHaveBeenCalledWith(1);
  });
});

describe('MobileScheduleCenter — 错误语义（三次复审 §30–§32）', () => {
  it('a reload failure KEEPS the already-loaded cards and shows a warning', async () => {
    mocks.fetchSchedules
      .mockResolvedValueOnce(SCHEDULES)
      .mockRejectedValueOnce(new Error('刷新失败'));
    await mountCenter();
    await flush(20);

    // Two real cards loaded…
    expect(document.querySelectorAll('.mobile-schedule-card')).toHaveLength(2);

    // …the next load (status switch here — same reload path) fails…
    await act(async () => {
      document.querySelector<HTMLButtonElement>('[data-value="running"]')!.click();
    });
    await flush(20);

    // …已加载的两张卡仍在（数据没有消失），partial 警告可见。
    expect(document.querySelectorAll('.mobile-schedule-card')).toHaveLength(2);
    expect(document.body.textContent).toContain('刷新失败');
    expect(document.body.textContent)
      .toContain('当前显示的是上次已加载数据');
  });

  it('a first-page failure with no data is a fatal error state, not an empty list', async () => {
    mocks.fetchSchedules.mockRejectedValue(new Error('网络错误'));
    await mountCenter();
    await flush(20);

    expect(document.body.textContent).toContain('加载定时任务失败');
    expect(document.body.textContent).not.toContain('还没有定时任务');
    // 没有任何卡片被渲染。
    expect(document.querySelectorAll('.mobile-schedule-card')).toHaveLength(0);
  });
});
