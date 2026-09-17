/**
 * Mobile surfaces — interaction regressions for the design report's §29.3 and
 * §29.4 test plans.
 *
 * These are behaviour tests, not snapshots: each one pins a rule the report
 * states explicitly and that is easy to break silently —
 *   · 最近使用 shows at most THREE, per selector;
 *   · an unmatched search shows the empty state and NOT everything (§15.3);
 *   · the agent sheet never lists a non-chat application (§16.1/§16.2);
 *   · send is refused while text is empty or an upload is still in flight (§19);
 *   · `+` and 技能 open their own sheets, and the 技能 button stays put even
 *     when the agent has no skills (§21).
 */
// @vitest-environment jsdom
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react-dom/test-utils';
import type { AgentSkill, V2Application } from '@/services/runApi';

import MobileCatalogSheet from '../MobileCatalogSheet';
import MobileComposer from '../MobileComposer';
import MobileSkillSheet from '../MobileSkillSheet';
import { MOBILE_RECENT_LIMIT, MOBILE_SHEET_MAX_ROWS } from '@/lib/mobileCatalog';

// ── jsdom shims antd needs ────────────────────────────────────────────
(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
};
(globalThis as unknown as { matchMedia: unknown }).matchMedia = (query: string) => ({
  matches: false,
  media: query,
  onchange: null,
  addListener() {},
  removeListener() {},
  addEventListener() {},
  removeEventListener() {},
  dispatchEvent: () => false,
});

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

/** React controlled inputs only fire onChange via the native setter. */
function setNativeValue(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const proto = el instanceof HTMLTextAreaElement
    ? HTMLTextAreaElement.prototype
    : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, 'value')!.set!.call(el, value);
  el.dispatchEvent(new Event('input', { bubbles: true }));
}

async function mount(node: React.ReactElement): Promise<{ host: HTMLElement; root: Root }> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  await act(async () => { root.render(node); });
  await flush(30);
  return { host, root };
}

const mounted: Array<{ host: HTMLElement; root: Root }> = [];
afterEach(async () => {
  while (mounted.length) {
    const { host, root } = mounted.pop()!;
    await act(async () => { root.unmount(); });
    host.remove();
  }
  document.body.innerHTML = '';
});

async function mountTracked(node: React.ReactElement) {
  const entry = await mount(node);
  mounted.push(entry);
  return entry;
}

/** Find a sheet row by its visible text. */
function rowByText(text: string): HTMLElement | undefined {
  return Array.from(document.querySelectorAll<HTMLElement>('.mobile-sheet__row'))
    .find((el) => (el.textContent || '').includes(text));
}

function buttonByLabel(label: string): HTMLElement | undefined {
  return Array.from(document.querySelectorAll<HTMLElement>('button'))
    .find((el) => (el.getAttribute('aria-label') || '').includes(label));
}

// ── fixtures ──────────────────────────────────────────────────────────

const app = (over: Partial<V2Application>): V2Application => ({
  id: 1, slug: 's', name: 'A', description: '', icon: '', kind: 'chat',
  runtime_type: 'agent', provider_key: 'feishu_aily', capabilities: {}, enabled: true,
  ...over,
});

const CATALOG: V2Application[] = [
  app({ id: 1, slug: 'wenshu', name: '问数小安', description: '数据问答助手',
    category_slug: 'data', category_name: '数据分析', last_used_at: '2026-09-15T00:00:00Z' }),
  app({ id: 2, slug: 'yunwei', name: '运维小安', description: 'IT 运维助手',
    category_slug: 'it', category_name: 'IT运维', last_used_at: '2026-09-14T00:00:00Z' }),
  app({ id: 3, slug: 'hetong', name: '合同助手', description: '合同分析助手',
    category_slug: 'hr', category_name: 'HR', last_used_at: '2026-09-13T00:00:00Z' }),
  app({ id: 4, slug: 'caizhi', name: '财税助手', description: '财税问答',
    category_slug: 'hr', category_name: 'HR', last_used_at: '2026-09-12T00:00:00Z' }),
  // Non-chat: must never appear in the agent sheet.
  app({ id: 10, slug: 'barcode', name: '条码流向查询', kind: 'task',
    category_slug: 'query', category_name: '查询' }),
  app({ id: 11, slug: 'oa', name: 'OA密码修改', kind: 'custom',
    category_slug: 'office', category_name: '办公' }),
];

const SKILLS: AgentSkill[] = [
  { id: 'income', name: '查询收入数据', description: '按月查询', prompt: '/查询收入查询' },
  { id: 'chart', name: '图表分析', description: '生成图表', prompt: '/图表分析' },
];

// ── MobileCatalogSheet (§29.3) ────────────────────────────────────────

describe('MobileCatalogSheet (§5–§6)', () => {
  const noop = () => {};

  it('lists chat agents and never a non-chat application', async () => {
    await mountTracked(
      <MobileCatalogSheet open type="agent" applications={CATALOG} recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    expect(document.body.textContent).toContain('问数小安');
    expect(document.body.textContent).not.toContain('条码流向查询');
    expect(document.body.textContent).not.toContain('OA密码修改');
  });

  it('lists only non-chat applications in the 应用 sheet', async () => {
    await mountTracked(
      <MobileCatalogSheet open type="app" applications={CATALOG} recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    expect(document.body.textContent).toContain('条码流向查询');
    expect(document.body.textContent).toContain('OA密码修改');
    expect(document.body.textContent).not.toContain('问数小安');
  });

  it('shows at most THREE 最近使用 rows', async () => {
    await mountTracked(
      <MobileCatalogSheet open type="agent" applications={CATALOG} recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    const label = Array.from(document.querySelectorAll('.mobile-sheet__section-label'))
      .find((el) => el.textContent === '最近使用');
    expect(label).toBeTruthy();

    // The 4 agents all carry last_used_at, so an uncapped recent list would
    // show all four. Only 3 may appear under the 最近使用 label.
    const recentRows: Element[] = [];
    let node = label!.nextElementSibling;
    while (node && !node.classList.contains('mobile-sheet__divider')) {
      if (node.classList.contains('mobile-sheet__row')) recentRows.push(node);
      node = node.nextElementSibling;
    }
    expect(recentRows).toHaveLength(3);
    expect(recentRows[0].textContent).toContain('问数小安');
  });

  it('keeps a large catalog bounded while 最近使用 still shows its own rows', async () => {
    // The production catalog is served WHOLE (no server-side pagination) and
    // the shared dev DB carries integration-test rows — 1800+ entries. Two
    // things must hold at that size, and both were broken at once:
    //   1. the sheet must not render one row per application;
    //   2. 最近使用 must still find its own rows, which means the slice cannot
    //      simply take the head of the raw catalog.
    const huge: V2Application[] = Array.from({ length: 600 }, (_, i) => app({
      id: 1000 + i, slug: `bulk-${i}`, name: `批量智能体${i}`,
      category_slug: 'bulk', category_name: '批量',
    }));
    const withRecent: V2Application[] = [
      ...huge,
      // Deliberately LAST: a head-only slice would drop the recent agent and
      // the trap below would go unnoticed.
      app({ id: 4242, slug: 'hot', name: '常用智能体',
        last_used_at: '2026-09-16T00:00:00Z' }),
    ];

    await mountTracked(
      <MobileCatalogSheet open type="agent" applications={withRecent} recentIds={[4242]}
        onClose={noop} onSelect={noop} />,
    );

    const rows = document.querySelectorAll('.mobile-sheet__row');
    expect(rows.length).toBeLessThanOrEqual(MOBILE_SHEET_MAX_ROWS + MOBILE_RECENT_LIMIT);
    expect(rowByText('常用智能体')).toBeTruthy();
    expect(document.body.textContent).toContain('加载更多');
  });

  it('expands by 加载更多 and hides the button once the list is complete', async () => {
    const bulk: V2Application[] = Array.from({ length: MOBILE_SHEET_MAX_ROWS + 5 }, (_, i) => app({
      id: 2000 + i, slug: `b-${i}`, name: `智能体${i}`, category_slug: 'bulk', category_name: '批量',
    }));

    await mountTracked(
      <MobileCatalogSheet open type="agent" applications={bulk} recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    const more = () => Array.from(document.querySelectorAll<HTMLElement>('.mobile-sheet__more-btn'))
      .find((el) => (el.textContent || '').includes('加载更多'));

    expect(document.querySelectorAll('.mobile-sheet__row')).toHaveLength(MOBILE_SHEET_MAX_ROWS);
    await click(more()!);
    expect(document.querySelectorAll('.mobile-sheet__row')).toHaveLength(bulk.length);
    // Nothing left to reveal → the affordance must disappear rather than sit
    // there as a dead button.
    expect(more()).toBeUndefined();
  });

  it('searches the WHOLE catalog, not just the rows currently rendered', async () => {
    // This is the trap the cap creates: capping the rendered rows must never
    // cap what search can find, or an admin loses access to agents that exist.
    const bulk: V2Application[] = Array.from({ length: MOBILE_SHEET_MAX_ROWS + 10 }, (_, i) => app({
      id: 3000 + i, slug: `c-${i}`, name: `智能体${i}`, category_slug: 'bulk', category_name: '批量',
    }));

    await mountTracked(
      <MobileCatalogSheet open type="agent" applications={bulk} recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    await click(buttonByLabel('搜索')!);
    await act(async () => {
      setNativeValue(document.querySelector('.mobile-sheet__search input')!, '智能体34');
    });
    await flush(30);

    // Beyond the first page, so it can only come from the full pool.
    expect(rowByText('智能体34')).toBeTruthy();
  });

  it('renders 全部 first, then the catalog categories', async () => {
    await mountTracked(
      <MobileCatalogSheet open type="agent" applications={CATALOG} recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    const tabs = Array.from(document.querySelectorAll('.mobile-sheet__tab'))
      .map((el) => el.textContent);
    expect(tabs[0]).toBe('全部');
    expect(tabs.slice(1)).toEqual(['数据分析', 'IT运维', 'HR']);
  });

  it('filters the list by category while leaving 最近使用 alone (§5.3)', async () => {
    await mountTracked(
      <MobileCatalogSheet open type="agent" applications={CATALOG} recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    const hrTab = Array.from(document.querySelectorAll<HTMLElement>('.mobile-sheet__tab'))
      .find((el) => el.textContent === 'HR');
    await click(hrTab!);

    // The section label survives…
    expect(Array.from(document.querySelectorAll('.mobile-sheet__section-label'))
      .some((el) => el.textContent === '最近使用')).toBe(true);
    // …and the list is narrowed to the HR rows.
    expect(document.body.textContent).toContain('合同助手');
    expect(document.body.textContent).toContain('财税助手');
  });

  it('searches name / description / category and hides 最近使用 (§5.4)', async () => {
    await mountTracked(
      <MobileCatalogSheet open type="agent" applications={CATALOG} recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    await click(buttonByLabel('搜索')!);
    const input = document.querySelector<HTMLInputElement>('.mobile-sheet__search input')!;
    await act(async () => { setNativeValue(input, 'IT'); });
    await flush(30);

    expect(document.body.textContent).toContain('运维小安');
    expect(document.body.textContent).not.toContain('合同助手');
    // Searching hides the recency block so results own the list.
    expect(Array.from(document.querySelectorAll('.mobile-sheet__section-label'))
      .some((el) => el.textContent === '最近使用')).toBe(false);
  });

  it('shows the empty state for an unmatched search — never everything (§15.3)', async () => {
    await mountTracked(
      <MobileCatalogSheet open type="agent" applications={CATALOG} recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    await click(buttonByLabel('搜索')!);
    const input = document.querySelector<HTMLInputElement>('.mobile-sheet__search input')!;
    await act(async () => { setNativeValue(input, 'zzzz'); });
    await flush(30);

    expect(document.body.textContent).toContain('没有找到');
    expect(document.body.textContent).not.toContain('问数小安');
  });

  it('marks the current application with ✓ and the main agent with 主', async () => {
    await mountTracked(
      <MobileCatalogSheet
        open
        type="agent"
        applications={[app({ id: 1, slug: 'w', name: '问数小安', is_default_agent: true }),
          app({ id: 2, slug: 'y', name: '运维小安' })]}
        recentIds={[]}
        activeApplicationId={2}
        onClose={noop}
        onSelect={noop}
      />,
    );

    const active = rowByText('运维小安')!;
    expect(active.querySelector('.mobile-sheet__tick')).toBeTruthy();
    expect(rowByText('问数小安')!.querySelector('.mobile-sheet__tick')).toBeNull();
    expect(rowByText('问数小安')!.textContent).toContain('主');
  });

  it('reports the picked application and closes', async () => {
    const onSelect = vi.fn();
    const onClose = vi.fn();
    await mountTracked(
      <MobileCatalogSheet open type="agent" applications={CATALOG} recentIds={[]}
        onClose={onClose} onSelect={onSelect} />,
    );

    await click(rowByText('合同助手')!);

    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect.mock.calls[0][0]).toMatchObject({ id: 3, slug: 'hetong' });
    expect(onClose).toHaveBeenCalled();
  });

  it('tells the user when there is nothing to pick (§15.2)', async () => {
    await mountTracked(
      <MobileCatalogSheet open type="app" applications={[]} recentIds={[]}
        onClose={noop} onSelect={noop} />,
    );

    expect(document.body.textContent).toContain('暂无可用应用');
    expect(document.body.textContent).toContain('请联系管理员配置');
  });

  it('never offers a disabled application', async () => {
    await mountTracked(
      <MobileCatalogSheet
        open
        type="agent"
        applications={[app({ id: 1, slug: 'a', name: '在用的' }),
          app({ id: 2, slug: 'b', name: '已停用的', enabled: false })]}
        recentIds={[]}
        onClose={noop}
        onSelect={noop}
      />,
    );

    expect(document.body.textContent).toContain('在用的');
    expect(document.body.textContent).not.toContain('已停用的');
  });
});

// ── MobileComposer (§29.4) ────────────────────────────────────────────

describe('MobileComposer (§8/§9/§11)', () => {
  const baseProps = {
    value: '',
    sending: false,
    uploads: [],
    supportsAttachment: true,
    skillLabel: '技能',
    hasSkills: true,
    onChange: () => {},
    onSend: () => {},
    onOpenAttachments: () => {},
    onOpenSkills: () => {},
    onRemoveUpload: () => {},
  };

  it('renders the two-layer structure with no microphone button (§8.1)', async () => {
    await mountTracked(<MobileComposer {...baseProps} />);

    expect(document.querySelector('.mobile-composer__textarea')).toBeTruthy();
    expect(document.querySelector('.mobile-composer__toolbar')).toBeTruthy();
    expect(document.body.textContent).toContain('技能');
    // 无麦克风假按钮 — the acceptance list forbids decorative entries.
    expect(buttonByLabel('麦克风')).toBeUndefined();
    expect(buttonByLabel('语音')).toBeUndefined();
  });

  it('refuses to send empty text', async () => {
    const onSend = vi.fn();
    await mountTracked(<MobileComposer {...baseProps} onSend={onSend} />);

    const send = buttonByLabel('发送')! as HTMLButtonElement;
    expect(send.disabled).toBe(true);

    await click(send);
    expect(onSend).not.toHaveBeenCalled();
  });

  it('sends once the text is non-empty', async () => {
    const onSend = vi.fn();
    const { root } = await mountTracked(
      <MobileComposer {...baseProps} value="本月收入多少" onSend={onSend} />,
    );

    const send = buttonByLabel('发送')! as HTMLButtonElement;
    expect(send.disabled).toBe(false);

    await act(async () => {
      root.render(<MobileComposer {...baseProps} value="本月收入多少" onSend={onSend} />);
    });
    await click(send);
    expect(onSend).toHaveBeenCalledTimes(1);
  });

  // §11: a file still uploading blocks the send and is NOT discarded.
  it('refuses to send while an upload is in flight, and keeps the chip', async () => {
    const onSend = vi.fn();
    await mountTracked(
      <MobileComposer
        {...baseProps}
        value="带附件的提问"
        uploads={[{ key: 'u1', name: 'sales.xlsx', state: 'uploading' }]}
        onSend={onSend}
      />,
    );

    const send = buttonByLabel('发送')! as HTMLButtonElement;
    expect(send.disabled).toBe(true);
    expect(document.body.textContent).toContain('sales.xlsx');
    expect(document.body.textContent).toContain('上传中');

    await click(send);
    expect(onSend).not.toHaveBeenCalled();
  });

  it('allows sending once the upload is ready', async () => {
    await mountTracked(
      <MobileComposer
        {...baseProps}
        value="带附件的提问"
        uploads={[{ key: 'u1', name: 'sales.pdf', state: 'ready', attachmentId: 'att-1' }]}
      />,
    );

    expect((buttonByLabel('发送')! as HTMLButtonElement).disabled).toBe(false);
  });

  it('disables everything while a run is streaming', async () => {
    await mountTracked(<MobileComposer {...baseProps} value="x" sending />);

    expect((buttonByLabel('发送')! as HTMLButtonElement).disabled).toBe(true);
    expect((document.querySelector('.mobile-composer__textarea') as HTMLTextAreaElement)
      .disabled).toBe(true);
  });

  it('opens the attachment sheet from + and the skill sheet from 技能 (§22)', async () => {
    const onOpenAttachments = vi.fn();
    const onOpenSkills = vi.fn();
    await mountTracked(
      <MobileComposer
        {...baseProps}
        onOpenAttachments={onOpenAttachments}
        onOpenSkills={onOpenSkills}
      />,
    );

    await click(buttonByLabel('添加图片或文件')!);
    expect(onOpenAttachments).toHaveBeenCalledTimes(1);

    const skillBtn = Array.from(document.querySelectorAll<HTMLElement>('button'))
      .find((el) => el.classList.contains('mobile-composer__skill-btn'))!;
    await click(skillBtn);
    expect(onOpenSkills).toHaveBeenCalledTimes(1);
  });

  // §21: hiding the button would make the toolbar jump between agents.
  it('keeps the 技能 button in place when the agent has no skills', async () => {
    await mountTracked(<MobileComposer {...baseProps} hasSkills={false} />);

    const skillBtn = document.querySelector('.mobile-composer__skill-btn');
    expect(skillBtn).toBeTruthy();
    expect(skillBtn!.classList.contains('mobile-composer__skill-btn--muted')).toBe(true);
  });

  it('hides + entirely when the application does not support attachments', async () => {
    await mountTracked(<MobileComposer {...baseProps} supportsAttachment={false} />);

    expect(buttonByLabel('添加图片或文件')).toBeUndefined();
  });

  it('shows the selection count as the skill label (§9.2)', async () => {
    await mountTracked(<MobileComposer {...baseProps} skillLabel="技能 2" />);
    expect(document.querySelector('.mobile-composer__skill-label')!.textContent)
      .toBe('技能 2');
  });

  it('removes an upload from its chip', async () => {
    const onRemoveUpload = vi.fn();
    await mountTracked(
      <MobileComposer
        {...baseProps}
        uploads={[{ key: 'u1', name: 'report.pdf', state: 'ready', attachmentId: 'a' }]}
        onRemoveUpload={onRemoveUpload}
      />,
    );

    await click(buttonByLabel('移除 report.pdf')!);
    expect(onRemoveUpload).toHaveBeenCalledWith('u1');
  });
});

// ── MobileSkillSheet (§29.4 技能) ─────────────────────────────────────

describe('MobileSkillSheet (§9.3)', () => {
  const skillNames = () => Array.from(
    document.querySelectorAll('.mobile-skill-row__name')).map((el) => el.textContent);

  it('multi-selects, lifts 已选 to the top and commits on 完成', async () => {
    const onChange = vi.fn();
    const onClose = vi.fn();
    await mountTracked(
      <MobileSkillSheet open skills={SKILLS} selectedIds={[]}
        onChange={onChange} onClose={onClose} />,
    );

    expect(document.body.textContent).toContain('选择技能');
    expect(skillNames()).toEqual(['查询收入数据', '图表分析']);

    // Selecting the SECOND catalog entry lifts it above the unselected one.
    await click(rowByText('图表分析')!);
    await flush(20);
    expect(skillNames()).toEqual(['图表分析', '查询收入数据']);

    // Selecting both puts each group back in CATALOG order — click order must
    // not decide the order, because the composed text (and therefore the
    // idempotency hash) has to be identical for identical selections.
    await click(rowByText('查询收入数据')!);
    await flush(20);
    expect(skillNames()).toEqual(['查询收入数据', '图表分析']);

    const done = Array.from(document.querySelectorAll<HTMLElement>('button'))
      .find((el) => el.classList.contains('mobile-skill-sheet__done'))!;
    expect(done.textContent).toContain('已选 2');
    await click(done);

    expect(onChange).toHaveBeenCalledWith(['income', 'chart']);
    expect(onClose).toHaveBeenCalled();
  });

  it('says so honestly when the agent has no skills configured (§21)', async () => {
    await mountTracked(
      <MobileSkillSheet open skills={[]} selectedIds={[]}
        onChange={() => {}} onClose={() => {}} />,
    );

    expect(document.body.textContent).toContain('当前智能体暂无可选技能');
    expect(document.body.textContent).not.toContain('完成');
  });
});
