/**
 * MobileConsole primitives — click / keyboard / stopPropagation / disabled /
 * danger / close behaviour (开发执行报告 §76).
 */
// @vitest-environment jsdom
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';

vi.mock('antd', () => ({
  Button: ({ children, onClick, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement>) => (
    <button type="button" onClick={onClick} {...props}>{children}</button>
  ),
  Drawer: ({ open, children }: { open: boolean; children: React.ReactNode }) => (
    open ? <aside data-testid="drawer">{children}</aside> : null
  ),
  Input: (props: React.InputHTMLAttributes<HTMLInputElement>) => (
    <input {...props} />
  ),
}));

import {
  MobileActionSheet,
  MobileEntityRow,
  MobileFullScreenDrawer,
  type MobileAction,
} from '../index';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

async function mount(node: React.ReactElement): Promise<{ host: HTMLElement; root: Root }> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  await act(async () => { root.render(node); });
  return { host, root };
}

function click(el: Element) {
  return act(async () => {
    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

function keyDown(el: Element, key: string) {
  return act(async () => {
    el.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }));
  });
}

afterEach(() => {
  document.body.innerHTML = '';
});

describe('MobileEntityRow', () => {
  const base = {
    avatar: <span data-testid="avatar" />,
    title: '财务助手',
    description: '财务数据查询',
    meta: '财务 · Aily',
  };

  it('fires onClick and renders the info hierarchy (§14)', async () => {
    const onClick = vi.fn();
    await mount(<MobileEntityRow {...base} onClick={onClick} />);
    const row = document.querySelector('.mobile-console-row')!;
    expect(row.textContent).toContain('财务助手');
    expect(row.textContent).toContain('财务数据查询');
    expect(row.textContent).toContain('财务 · Aily');
    expect(document.querySelector('.mobile-console-row__chevron')).toBeTruthy();
    await click(row);
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it('favorite click stops propagation and keeps its own aria-label (§15/§65)', async () => {
    const onClick = vi.fn();
    const onFavorite = vi.fn();
    await mount(
      <MobileEntityRow {...base} favorite onClick={onClick} onFavorite={onFavorite} />,
    );
    const star = document.querySelector('.mobile-console-row__star')!;
    expect(star.getAttribute('aria-label')).toBe('取消收藏：财务助手');
    await click(star);
    expect(onFavorite).toHaveBeenCalledTimes(1);
    expect(onClick).not.toHaveBeenCalled();
  });

  it('favorite supports keyboard activation without leaking to the row', async () => {
    const onClick = vi.fn();
    const onFavorite = vi.fn();
    await mount(
      <MobileEntityRow {...base} favorite={false} onClick={onClick} onFavorite={onFavorite} />,
    );
    const star = document.querySelector('.mobile-console-row__star')!;
    expect(star.getAttribute('aria-label')).toBe('收藏：财务助手');
    await keyDown(star, 'Enter');
    expect(onFavorite).toHaveBeenCalledTimes(1);
    expect(onClick).not.toHaveBeenCalled();
  });

  it('••• replaces the chevron and stops propagation (§37)', async () => {
    const onClick = vi.fn();
    const onMore = vi.fn();
    await mount(<MobileEntityRow {...base} onMore={onMore} onClick={onClick} />);
    const more = document.querySelector('.mobile-console-row__more')!;
    expect(more.getAttribute('aria-label')).toBe('更多操作：财务助手');
    expect(document.querySelector('.mobile-console-row__chevron')).toBeNull();
    await click(more);
    expect(onMore).toHaveBeenCalledTimes(1);
    expect(onClick).not.toHaveBeenCalled();
  });

  it('status dot + label renders the enterprise variant (§36)', async () => {
    await mount(
      <MobileEntityRow {...base} statusDot="on" statusLabel="启用" onClick={() => {}} />,
    );
    const status = document.querySelector('.mobile-console-row__status')!;
    expect(status.textContent).toContain('启用');
    expect(status.querySelector('.mobile-console-row__dot--on')).toBeTruthy();
  });
});

describe('MobileActionSheet (§58)', () => {
  const actions: MobileAction[] = [
    { key: 'run', label: '立即运行', onClick: vi.fn() },
    { key: 'remove', label: '删除', danger: true, onClick: vi.fn() },
    { key: 'off', label: '不可用', disabled: true, onClick: vi.fn() },
  ];

  it('renders only while open, closes on action, fires exactly that action', async () => {
    const onClose = vi.fn();
    const { root } = await mount(
      <MobileActionSheet open={false} actions={actions} onClose={onClose} />,
    );
    expect(document.querySelector('[data-testid="drawer"]')).toBeNull();

    await act(async () => {
      root.render(<MobileActionSheet open actions={actions} onClose={onClose} />);
    });
    const rows = Array.from(document.querySelectorAll('.mobile-action-sheet__row'));
    expect(rows).toHaveLength(3);

    await click(rows[0]);
    expect(actions[0].onClick).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('disabled actions neither fire nor close', async () => {
    const onClose = vi.fn();
    await mount(<MobileActionSheet open actions={actions} onClose={onClose} />);
    const disabled = document.querySelector<HTMLButtonElement>('.mobile-action-sheet__row[disabled]')!;
    expect(disabled.textContent).toContain('不可用');
    await click(disabled);
    expect(actions[2].onClick).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
  });

  it('marks the danger action (§27 删除)', async () => {
    await mount(<MobileActionSheet open actions={actions} onClose={() => {}} />);
    expect(document.querySelector('.mobile-action-sheet__row--danger')!.textContent)
      .toContain('删除');
  });
});

describe('MobileFullScreenDrawer (§29)', () => {
  it('renders header (back / title / action) and body, back closes', async () => {
    const onClose = vi.fn();
    const onAction = vi.fn();
    await mount(
      <MobileFullScreenDrawer
        open
        title="新建定时任务"
        actionText="保存"
        onAction={onAction}
        onClose={onClose}
      >
        <div data-testid="body">表单</div>
      </MobileFullScreenDrawer>,
    );
    expect(document.querySelector('.mobile-fs-drawer__title')!.textContent)
      .toBe('新建定时任务');
    expect(document.querySelector('[data-testid="body"]')).toBeTruthy();

    await click(document.querySelector('[aria-label="返回"]')!);
    expect(onClose).toHaveBeenCalledTimes(1);

    const save = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === '保存')!;
    await click(save);
    expect(onAction).toHaveBeenCalledTimes(1);
  });

  it('keeps the header balanced without a trailing action', async () => {
    await mount(
      <MobileFullScreenDrawer open title="详情" onClose={() => {}}>
        <div />
      </MobileFullScreenDrawer>,
    );
    expect(document.querySelector('.mobile-fs-drawer__header .mobile-shell__bar-spacer'))
      .toBeTruthy();
  });
});
