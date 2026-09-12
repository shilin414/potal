import { describe, expect, it } from 'vitest';
import { parseMention, routeMention } from '../mentionRouter';
import type { MentionCandidate } from '../mentionRouter';

const sales: MentionCandidate = { id: 1, slug: 'sales-assistant', name: '销售助手', kind: 'chat' };
const purchase: MentionCandidate = { id: 2, slug: 'purchase-assistant', name: '采购助手', kind: 'chat' };
const changeOa: MentionCandidate = { id: 3, slug: 'change-oa-password', name: '修改OA密码', kind: 'custom' };
/** Name with a space, to prove prefix matching beats first-token splitting. */
const dataApp: MentionCandidate = { id: 4, slug: 'data-helper', name: 'Data Helper', kind: 'chat' };
/** A shorter name that is a prefix of another name. */
const salesShort: MentionCandidate = { id: 5, slug: 'sales', name: '销售', kind: 'chat' };

const candidates = [sales, purchase, changeOa, dataApp, salesShort];

describe('parseMention', () => {
  it('ignores text without a leading @', () => {
    expect(parseMention('帮我分析一下', candidates))
      .toEqual({ mentioned: false, match: null });
    expect(parseMention('', candidates)).toEqual({ mentioned: false, match: null });
  });

  it('resolves an @name and preserves the rest verbatim', () => {
    const parsed = parseMention('@销售助手 帮我分析一下这个客户', candidates);
    expect(parsed.match?.application.id).toBe(sales.id);
    expect(parsed.match?.content).toBe('帮我分析一下这个客户');
  });

  it('resolves by slug', () => {
    const parsed = parseMention('@purchase-assistant 查下库存', candidates);
    expect(parsed.match?.application.id).toBe(purchase.id);
    expect(parsed.match?.content).toBe('查下库存');
  });

  it('is case insensitive for slugs and latin names', () => {
    expect(parseMention('@Data-Helper 跑个数', candidates).match?.application.id)
      .toBe(dataApp.id);
    expect(parseMention('@data helper 跑个数', candidates).match?.application.id)
      .toBe(dataApp.id);
  });

  it('prefers the longest matching name', () => {
    expect(parseMention('@销售助手 分析', candidates).match?.application.id)
      .toBe(sales.id);
    expect(parseMention('@销售 分析', candidates).match?.application.id)
      .toBe(salesShort.id);
  });

  it('keeps multi-line instructions intact', () => {
    const parsed = parseMention('@销售助手 第一行\n第二行', candidates);
    expect(parsed.match?.content).toBe('第一行\n第二行');
  });

  it('reports an unknown mention without matching', () => {
    expect(parseMention('@测试一下 你好', candidates))
      .toEqual({ mentioned: true, match: null });
  });

  it('does not route an @ that is not leading', () => {
    expect(parseMention('价格是 a@b.com', candidates))
      .toEqual({ mentioned: false, match: null });
    expect(parseMention('你好 @销售助手', candidates))
      .toEqual({ mentioned: false, match: null });
  });
});

describe('routeMention', () => {
  it('passes a plain message through to the current application', () => {
    expect(routeMention({ text: '你好', candidates, activeApplicationId: 1 }))
      .toEqual({ action: 'passthrough' });
  });

  it('sends in place when the mention targets the active application', () => {
    expect(routeMention({
      text: '@销售助手 帮我分析', candidates, activeApplicationId: sales.id,
    })).toEqual({ action: 'send', content: '帮我分析' });
  });

  it('switches application and carries the original instruction', () => {
    expect(routeMention({
      text: '@采购助手 查下库存', candidates, activeApplicationId: sales.id,
    })).toEqual({ action: 'switch', applicationId: purchase.id, content: '查下库存' });
  });

  it('switches onto another application from the idle home composer', () => {
    expect(routeMention({
      text: '@采购助手 查下库存', candidates, activeApplicationId: sales.id,
    }).action).toBe('switch');
    expect(routeMention({
      text: '@采购助手 查下库存', candidates, activeApplicationId: null,
    })).toEqual({ action: 'switch', applicationId: purchase.id, content: '查下库存' });
  });

  it('opens a non-chat application instead of sending', () => {
    expect(routeMention({
      text: '@修改OA密码', candidates, activeApplicationId: sales.id,
    })).toEqual({ action: 'open', applicationId: changeOa.id });
  });

  it('ignores a bare mention of the active application', () => {
    expect(routeMention({
      text: '@销售助手', candidates, activeApplicationId: sales.id,
    })).toEqual({ action: 'ignore' });
  });

  it('switches without sending when the mention is bare', () => {
    expect(routeMention({
      text: '@采购助手', candidates, activeApplicationId: sales.id,
    })).toEqual({ action: 'switch', applicationId: purchase.id, content: '' });
  });

  it('falls back to the default main agent for an unknown @', () => {
    const decision = routeMention({
      text: '@测试一下 帮我看看', candidates, activeApplicationId: sales.id,
    });
    expect(decision).toEqual({ action: 'passthrough' });
  });

  it('never throws on an empty candidate list', () => {
    expect(routeMention({ text: '@销售助手 hi', candidates: [], activeApplicationId: null }))
      .toEqual({ action: 'passthrough' });
  });
});
