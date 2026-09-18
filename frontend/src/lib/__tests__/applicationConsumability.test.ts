import { describe, expect, it } from 'vitest';
import { applicationConsumeBlock, consumeBlockMessage } from '@/lib/applicationConsumability';

describe('management consume blocking messages', () => {
  it.each([
    ['disabled', '该应用已停用，请先启用'],
    ['unbound', '该智能体尚未配置可用运行时'],
    ['runtime_unavailable', '当前运行时不可用，请检查 Provider 配置'],
  ] as const)('maps %s to an actionable management hint', (reason, expected) => {
    expect(consumeBlockMessage(reason)).toBe(expected);
  });

  it('allows only applications explicitly admitted by the backend', () => {
    expect(applicationConsumeBlock({ is_consumable: true })).toBeNull();
    expect(applicationConsumeBlock({ is_consumable: false, consume_block_reason: 'disabled' }))
      .toBe('该应用已停用，请先启用');
    expect(applicationConsumeBlock({})).toBe('该应用当前不可运行，请检查配置');
  });
});
