/**
 * agentSkills — the ONE place that turns a skill selection into message text.
 *
 * A skill is a named prompt fragment the composer PREPENDS to what the user
 * typed (「/查询收入查询」+「本月收入多少」). Nothing else in the request
 * changes: there is no `skill_refs` field, no provider capability and no new
 * idempotency input, because the fragment is carried by `content` — which the
 * existing `RunRequestHash` already covers. Two sends that differ only in
 * their selected skills therefore hash differently, which is exactly right.
 *
 * Pure functions only, so the composition rules are testable without React and
 * without a backend.
 */
import type { AgentSkill } from '@/services/runApi';

/** The design's cap on the composer badge (§9.2). */
export const SKILL_BADGE_MAX = 9;

/**
 * Resolve a stored selection against the agent's CURRENT skills.
 *
 * Two jobs:
 *   1. DROP ids the agent no longer offers, so an admin deleting a skill
 *      cannot make the composer send a fragment that no longer exists;
 *   2. RE-ORDER into catalog order, deliberately discarding the click order.
 *
 * (2) is load-bearing rather than cosmetic: the composed `content` is the
 * idempotency input, so two identical selections must always produce
 * byte-identical text. Honouring click order would let the same logical
 * request hash two ways depending on which chip the user tapped first.
 */
export function resolveSelectedSkills(
  available: AgentSkill[] | undefined,
  selectedIds: string[],
): AgentSkill[] {
  if (!available?.length || !selectedIds.length) return [];
  const wanted = new Set(selectedIds);
  return available.filter((skill) => wanted.has(skill.id));
}

/**
 * Prepend the selected skills' prompts to the user's message.
 *
 * Order: one prompt per line, catalog order, then the user's text verbatim on
 * its own line — so `本月收入多少` stays intact and readable in the transcript
 * and in the provider's own logs.
 *
 * No selection returns the content UNCHANGED (not merely trimmed), so a
 * skill-free send is byte-identical to what older builds produced.
 */
export function buildSkillAugmentedContent(
  selected: AgentSkill[],
  content: string,
): string {
  const prompts = selected
    .map((skill) => (skill.prompt || '').trim())
    .filter(Boolean);
  if (!prompts.length) return content;
  return [...prompts, content].join('\n');
}

/**
 * Composer chip label (§9.2/§21):
 *
 *   0 selected   → 技能
 *   1 selected   → the skill's name
 *   N selected   → 技能 N, saturating at 技能 9+
 */
export function skillButtonLabel(selected: AgentSkill[]): string {
  if (!selected.length) return '技能';
  if (selected.length === 1) return selected[0].name || '技能';
  if (selected.length > SKILL_BADGE_MAX) return `技能 ${SKILL_BADGE_MAX}+`;
  return `技能 ${selected.length}`;
}

/**
 * Order a skill list for the sheet: selected first, each group keeping its
 * catalog order (§9.3 「已选技能置顶」).
 */
export function orderSkillsForSheet(
  available: AgentSkill[],
  selectedIds: string[],
): AgentSkill[] {
  const wanted = new Set(selectedIds);
  const picked: AgentSkill[] = [];
  const rest: AgentSkill[] = [];
  available.forEach((skill) => {
    (wanted.has(skill.id) ? picked : rest).push(skill);
  });
  return [...picked, ...rest];
}

/** Toggle one skill in a selection, preserving catalog order (see above). */
export function toggleSkillSelection(
  available: AgentSkill[],
  selectedIds: string[],
  skillId: string,
): string[] {
  const next = selectedIds.includes(skillId)
    ? selectedIds.filter((id) => id !== skillId)
    : [...selectedIds, skillId];
  // Re-project through the catalog so the returned array is always in catalog
  // order — the same invariant resolveSelectedSkills relies on.
  const wanted = new Set(next);
  return available.filter((skill) => wanted.has(skill.id)).map((skill) => skill.id);
}
