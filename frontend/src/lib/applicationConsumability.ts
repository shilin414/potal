import type { ConsumeBlockReason } from '@/services/runApi';

interface ConsumabilitySnapshot {
  is_consumable?: boolean;
  consume_block_reason?: ConsumeBlockReason;
}

const BLOCK_MESSAGES: Record<ConsumeBlockReason, string> = {
  disabled: '该应用已停用，请先启用',
  unbound: '该智能体尚未配置可用运行时',
  runtime_unavailable: '当前运行时不可用，请检查 Provider 配置',
};

export function consumeBlockMessage(reason?: ConsumeBlockReason): string {
  return reason ? BLOCK_MESSAGES[reason] : '该应用当前不可运行，请检查配置';
}

export function applicationConsumeBlock(application: ConsumabilitySnapshot): string | null {
  return application.is_consumable === true
    ? null
    : consumeBlockMessage(application.consume_block_reason);
}
