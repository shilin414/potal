/**
 * composerRouting — `@mention` routing with SERVER-resolved candidates
 * (执行报告 §14, P1-2).
 *
 * The property worth pinning is negative as much as positive: a message with
 * no leading `@` must cost ZERO requests (this path runs on every send), and
 * an unreachable resolver must degrade to "send as typed" rather than blocking
 * the composer. The positive property is that a mention resolves even for an
 * application the browser has never loaded — which is the whole reason the
 * candidate array stopped being built from the catalog.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  resolveApplicationMention: vi.fn(),
}));

vi.mock('@/services/runApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/services/runApi')>()),
  resolveApplicationMention: mocks.resolveApplicationMention,
}));

import { mentionToken, routeComposerText } from '@/lib/composerRouting';

const chat = (id: number, name: string, slug = `s-${id}`) => (
  { id, slug, name, kind: 'chat' });
const fixed = (id: number, name: string, slug = `s-${id}`) => (
  { id, slug, name, kind: 'custom' });

beforeEach(() => { mocks.resolveApplicationMention.mockReset(); });

describe('mentionToken', () => {
  it('reads the token after a leading @ and nothing else', () => {
    expect(mentionToken('@销售助手 帮我查一下')).toBe('销售助手');
    expect(mentionToken('  @sales   data')).toBe('sales');
    expect(mentionToken('@销售助手')).toBe('销售助手');
    expect(mentionToken('这是 @销售助手')).toBe('');
    expect(mentionToken('no mention')).toBe('');
  });
});

describe('routeComposerText', () => {
  it('passes plain text straight through without a request', async () => {
    const { route, target } = await routeComposerText('今天天气如何', 5);

    expect(route).toEqual({ action: 'passthrough' });
    expect(target).toBeUndefined();
    expect(mocks.resolveApplicationMention).not.toHaveBeenCalled();
  });

  it('leaves an inline @ alone (only a LEADING mention routes)', async () => {
    await routeComposerText('请转给 @销售助手 处理', 5);

    expect(mocks.resolveApplicationMention).not.toHaveBeenCalled();
  });

  it('switches workspace and strips the mention for a chat agent', async () => {
    mocks.resolveApplicationMention.mockResolvedValue([chat(7, '销售助手')]);

    const { route, target } = await routeComposerText('@销售助手 查一下本月数据', 5);

    expect(mocks.resolveApplicationMention).toHaveBeenCalledWith('销售助手');
    expect(route).toEqual({
      action: 'switch', applicationId: 7, content: '查一下本月数据',
    });
    // The candidate travels with the decision so the caller can navigate
    // without a second lookup.
    expect(target).toMatchObject({ id: 7, slug: 's-7' });
  });

  it('opens (not sends) a non-chat application', async () => {
    mocks.resolveApplicationMention.mockResolvedValue([fixed(9, '修改OA密码')]);

    const { route, target } = await routeComposerText('@修改OA密码', 5);

    expect(route).toEqual({ action: 'open', applicationId: 9 });
    expect(target).toMatchObject({ id: 9 });
  });

  it('sends in place when the mentioned agent is already active', async () => {
    mocks.resolveApplicationMention.mockResolvedValue([chat(7, '销售助手')]);

    const { route } = await routeComposerText('@销售助手 继续', 7);

    expect(route).toEqual({ action: 'send', content: '继续' });
  });

  it('ignores a bare mention of the active agent', async () => {
    mocks.resolveApplicationMention.mockResolvedValue([chat(7, '销售助手')]);

    const { route } = await routeComposerText('@销售助手', 7);

    expect(route).toEqual({ action: 'ignore' });
  });

  it('routes a mention of an agent the browser has never loaded', async () => {
    // The server answers from the whole catalog, so the id/slug are new here:
    // nothing local could have produced this candidate.
    mocks.resolveApplicationMention.mockResolvedValue([chat(4242, '财务助手')]);

    const { route, target } = await routeComposerText('@财务助手 出一份月报', 5);

    expect(route).toMatchObject({ action: 'switch', applicationId: 4242 });
    expect(target?.slug).toBe('s-4242');
  });

  it('falls through to passthrough for an unknown mention', async () => {
    mocks.resolveApplicationMention.mockResolvedValue([]);

    const { route } = await routeComposerText('@不存在的智能体 你好', 5);

    // §38: an unresolvable mention must never surface an error — the raw text
    // goes to the current agent, mention and all.
    expect(route).toEqual({ action: 'passthrough' });
  });

  it('degrades to passthrough when the resolver is unreachable', async () => {
    mocks.resolveApplicationMention.mockRejectedValue(new Error('offline'));

    const { route } = await routeComposerText('@销售助手 查数据', 5);

    expect(route).toEqual({ action: 'passthrough' });
  });
});
