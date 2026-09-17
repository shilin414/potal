import { describe, expect, it } from 'vitest';
import {
  SKILL_BADGE_MAX,
  buildSkillAugmentedContent,
  orderSkillsForSheet,
  resolveSelectedSkills,
  skillButtonLabel,
  toggleSkillSelection,
} from '../agentSkills';
import type { AgentSkill } from '@/services/runApi';
import { routeMention } from '../mentionRouter';

const skill = (over: Partial<AgentSkill>): AgentSkill => ({
  id: 's', name: '技能', description: '', prompt: '/p', ...over,
});

const INCOME = skill({ id: 'income', name: '查询收入数据', prompt: '/查询收入查询' });
const CHART = skill({ id: 'chart', name: '图表分析', prompt: '/图表分析' });
const DOC = skill({ id: 'doc', name: '文档总结', prompt: '/文档总结' });
const CATALOG = [INCOME, CHART, DOC];

describe('resolveSelectedSkills', () => {
  it('returns the selected skills', () => {
    expect(resolveSelectedSkills(CATALOG, ['income'])).toEqual([INCOME]);
  });

  it('drops an id the agent no longer offers', () => {
    expect(resolveSelectedSkills(CATALOG, ['income', 'deleted'])).toEqual([INCOME]);
  });

  it('ignores a stored selection when the agent offers nothing', () => {
    expect(resolveSelectedSkills(undefined, ['income'])).toEqual([]);
    expect(resolveSelectedSkills([], ['income'])).toEqual([]);
  });

  // The composed content IS the idempotency input, so the same selection must
  // always produce the same text regardless of the order chips were tapped.
  it('is insensitive to the stored selection order', () => {
    const a = resolveSelectedSkills(CATALOG, ['income', 'chart']);
    const b = resolveSelectedSkills(CATALOG, ['chart', 'income']);
    expect(a).toEqual(b);
    expect(a.map((s) => s.id)).toEqual(['income', 'chart']);
  });
});

describe('buildSkillAugmentedContent', () => {
  it('leaves the content byte-identical when nothing is selected', () => {
    const raw = '本月收入多少';
    expect(buildSkillAugmentedContent([], raw)).toBe(raw);
  });

  it('prepends the prompt on its own line, keeping the user text verbatim', () => {
    expect(buildSkillAugmentedContent([INCOME], '本月收入多少'))
      .toBe('/查询收入查询\n本月收入多少');
  });

  it('prepends several prompts in catalog order', () => {
    expect(buildSkillAugmentedContent([INCOME, CHART], '做个报表'))
      .toBe('/查询收入查询\n/图表分析\n做个报表');
  });

  it('skips a skill whose prompt is blank rather than emitting an empty line', () => {
    const blank = skill({ id: 'blank', name: '空', prompt: '   ' });
    expect(buildSkillAugmentedContent([blank, INCOME], 'x'))
      .toBe('/查询收入查询\nx');
  });

  it('trims the prompt but never the user content', () => {
    const padded = skill({ id: 'p', name: 'P', prompt: '  /padded  ' });
    expect(buildSkillAugmentedContent([padded], '  keep me  '))
      .toBe('/padded\n  keep me  ');
  });
});

describe('skillButtonLabel (§9.2/§21)', () => {
  it('reads 技能 with no selection', () => {
    expect(skillButtonLabel([])).toBe('技能');
  });

  it('reads the skill name when exactly one is selected', () => {
    expect(skillButtonLabel([INCOME])).toBe('查询收入数据');
  });

  it('reads 技能 N for a multi-selection', () => {
    expect(skillButtonLabel([INCOME, CHART])).toBe('技能 2');
  });

  it(`saturates at 技能 ${SKILL_BADGE_MAX}+`, () => {
    const nine = Array.from({ length: 9 }, (_, i) => skill({ id: `s${i}`, name: `s${i}` }));
    expect(skillButtonLabel(nine)).toBe('技能 9');
    const ten = Array.from({ length: 10 }, (_, i) => skill({ id: `s${i}`, name: `s${i}` }));
    expect(skillButtonLabel(ten)).toBe('技能 9+');
  });

  it('never renders an empty label for a nameless single skill', () => {
    expect(skillButtonLabel([skill({ id: 'x', name: '', prompt: '/x' })])).toBe('技能');
  });
});

describe('orderSkillsForSheet (§9.3 已选置顶)', () => {
  it('lifts the selected skills, keeping catalog order inside each group', () => {
    expect(orderSkillsForSheet(CATALOG, ['doc']).map((s) => s.id))
      .toEqual(['doc', 'income', 'chart']);
  });

  it('returns the catalog order when nothing is selected', () => {
    expect(orderSkillsForSheet(CATALOG, []).map((s) => s.id))
      .toEqual(['income', 'chart', 'doc']);
  });
});

describe('toggleSkillSelection', () => {
  it('adds and removes', () => {
    expect(toggleSkillSelection(CATALOG, [], 'chart')).toEqual(['chart']);
    expect(toggleSkillSelection(CATALOG, ['chart'], 'chart')).toEqual([]);
  });

  it('keeps the result in catalog order, not click order', () => {
    let selected = toggleSkillSelection(CATALOG, [], 'doc');
    selected = toggleSkillSelection(CATALOG, selected, 'income');
    expect(selected).toEqual(['income', 'doc']);
  });

  it('ignores an id the agent does not offer', () => {
    expect(toggleSkillSelection(CATALOG, [], 'nope')).toEqual([]);
  });
});

/**
 * ORDERING REGRESSION.
 *
 * `parseMention` only routes a LEADING `@mention`. So prepending the skill
 * prompts BEFORE routing pushes the mention off the front and the instruction
 * silently stops routing — it would fall through as `passthrough` and be sent
 * to whatever agent happens to be active, which is the exact failure mode the
 * ordering in RunChatPanel.handleSend exists to prevent.
 *
 * This test pins both halves: the hazard is real, and the chosen order avoids
 * it while still delivering the skill prefix.
 */
describe('技能 + @mention ordering (RunChatPanel.handleSend contract)', () => {
  const candidates = [
    { id: 7, slug: 'sales', name: '销售助手', kind: 'chat' },
    { id: 8, slug: 'oa', name: 'OA密码修改', kind: 'custom' },
  ];

  it('routing FIRST keeps the @mention working', () => {
    const raw = '@销售助手 分析这个客户';

    const decision = routeMention({
      text: raw,
      candidates,
      activeApplicationId: 1, // a different agent is active
    });

    expect(decision).toEqual({
      action: 'switch', applicationId: 7, content: '分析这个客户',
    });

    // And the skill prefix is then added to the ROUTED content.
    const outgoing = buildSkillAugmentedContent([INCOME], '分析这个客户');
    expect(outgoing).toBe('/查询收入查询\n分析这个客户');
  });

  it('prepending BEFORE routing would have broken it (the bug this guards)', () => {
    const raw = '@销售助手 分析这个客户';
    const naive = buildSkillAugmentedContent([INCOME], raw);

    // The mention is no longer leading, so routing degrades to passthrough:
    // the instruction would go to the WRONG agent instead of 销售助手.
    expect(routeMention({ text: naive, candidates, activeApplicationId: 1 }))
      .toEqual({ action: 'passthrough' });
  });
});
