import { beforeEach, describe, expect, it, vi } from 'vitest';
import { validateAttachment, ATTACHMENT_LIMITS, getRun, fetchRunArtifacts } from '@/services/runApi';
import { applyEvent, finalizeRun, useRunChatStore } from '../useRunChatStore';
import type { RunEventRecord } from '@/services/runApi';
import type { RunChatState } from '../useRunChatStore';

// finalizeRun reconciles against GET /runs/:id; only that call is faked,
// the pure validators above stay real.
vi.mock('@/services/runApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/runApi')>();
  return {
    ...actual,
    getRun: vi.fn(),
    fetchRunArtifacts: vi.fn(async () => []),
  };
});

const fiveMb = 5 * 1024 * 1024;
const fortyMb = 40 * 1024 * 1024;

function file(name: string, size: number, type = 'image/png'): File {
  return new File([new Uint8Array(size)], name, { type });
}

describe('validateAttachment (official provider limits)', () => {
  it('accepts png/jpg/pdf within size limits', () => {
    expect(validateAttachment(file('a.png', 1000))).toBeNull();
    expect(validateAttachment(file('b.jpg', 1000, 'image/jpeg'))).toBeNull();
    expect(validateAttachment(file('c.pdf', 1000, 'application/pdf'))).toBeNull();
    expect(validateAttachment(file('d.PNG', 1000))).toBeNull();
  });

  it('rejects disallowed extensions', () => {
    expect(validateAttachment(file('virus.exe', 100))?.reason)
      .toContain('png / jpg / pdf');
    expect(validateAttachment(file('doc.docx', 100))?.reason)
      .toContain('png / jpg / pdf');
  });

  it('rejects images over 5MB', () => {
    expect(validateAttachment(file('big.png', fiveMb + 1))?.reason)
      .toContain('5MB');
  });

  it('rejects non-image files over 40MB', () => {
    expect(validateAttachment(file('big.pdf', fortyMb + 1, 'application/pdf'))?.reason)
      .toContain('40MB');
  });

  it('caps per-run attachments at 8', () => {
    expect(ATTACHMENT_LIMITS.maxPerRun).toBe(8);
  });
});

describe('applyEvent (unified event protocol rendering)', () => {
  const runId = 'run-1';

  const stateWithStream = (): RunChatState => ({
    ...useRunChatStore.getState(),
    conversations: {
      42: {
        id: 42,
        title: 't',
        messages: [
          { id: 'user-run-1', role: 'user', content: 'hi', created_at: '' },
          {
            id: 'run-run-1', role: 'assistant', content: '',
            created_at: '', runId, status: 'streaming', artifacts: [],
          },
        ],
        activeRunId: runId,
      },
    },
  });

  const event = (type: string, payload: Record<string, any>): RunEventRecord => ({
    run_id: runId,
    sequence: 1,
    event_type: type,
    payload,
  });

  const reduce = (state: RunChatState, evt: RunEventRecord): RunChatState =>
    ({ ...state, ...applyEvent(state, evt) } as RunChatState);

  it('content.delta appends incrementally', () => {
    let state = stateWithStream();
    state = reduce(state, event('content.delta', { text: '你' }));
    expect(state.conversations[42].messages[1].content).toBe('你');
    state = reduce(state, event('content.delta', { text: '好' }));
    expect(state.conversations[42].messages[1].content).toBe('你好');
  });

  it('artifact.discovered pins an artifact card with the local id', () => {
    let state = stateWithStream();
    state = reduce(state, event('artifact.discovered', {
      artifact_id: 'art-1', external_artifact_id: 'ext-1',
      provider_artifact_type: 'sandbox_file', name: 'report.pdf',
    }));
    const artifacts = state.conversations[42].messages[1].artifacts;
    expect(artifacts).toHaveLength(1);
    expect(artifacts![0]).toMatchObject({
      artifactId: 'art-1', name: 'report.pdf', normalizedType: 'sandbox_file',
    });
    // duplicate discovery must not duplicate the card
    state = reduce(state, event('artifact.discovered', {
      artifact_id: 'art-1', provider_artifact_type: 'sandbox_file',
    }));
    expect(state.conversations[42].messages[1].artifacts).toHaveLength(1);
  });

  it('run.completed replaces content with the reconciled text and closes the run', () => {
    let state = stateWithStream();
    state = reduce(state, event('content.delta', { text: '流式片段' }));
    state = reduce(state, event('run.completed', { status: 'succeeded', text: '最终答案' }));
    const msg = state.conversations[42].messages[1];
    expect(msg.content).toBe('最终答案');
    expect(msg.status).toBe('done');
    expect(state.conversations[42].activeRunId).toBeNull();
  });

  it('run.completed without text keeps accumulated deltas', () => {
    let state = stateWithStream();
    state = reduce(state, event('content.delta', { text: '流式片段' }));
    state = reduce(state, event('run.completed', { status: 'succeeded' }));
    expect(state.conversations[42].messages[1].content).toBe('流式片段');
  });

  it('run.failed surfaces the provider error', () => {
    const state = reduce(
      stateWithStream(),
      event('run.failed', {
        status: 'failed', error_code: 'aily_auth_error', error_message: 'no uat',
      }),
    );
    const msg = state.conversations[42].messages[1];
    expect(msg.status).toBe('failed');
    expect(msg.error).toBe('no uat');
  });

  // Execution Correctness Closure (T3 前端侧): retry 是非终态 ——
  // run.retrying 不得关闭流、不得清 activeRunId、不得改 status。
  it('run.retrying keeps the run active and streaming', () => {
    let state = stateWithStream();
    state = reduce(state, event('content.delta', { text: '部分回答' }));
    state = reduce(state, event('run.retrying', {
      attempt: 1, max_attempts: 3, reason: 'aily_rate_limit',
    }));
    const conv = state.conversations[42];
    const msg = conv.messages[1];
    expect(msg.status).toBe('streaming'); // NOT done / failed
    expect(conv.activeRunId).toBe(runId); // stream stays attached
    expect(msg.content).toBe('部分回答'); // accumulated content preserved
    expect(msg.retryNotice).toBeTruthy();
  });

  it('run.retrying followed by run.completed converges normally', () => {
    let state = stateWithStream();
    state = reduce(state, event('run.retrying', { attempt: 1, max_attempts: 3 }));
    state = reduce(state, event('content.chunk', { text: '重试后的回答', snapshot: '重试后的回答' }));
    state = reduce(state, event('run.completed', { status: 'succeeded', text: '重试后的回答' }));
    const conv = state.conversations[42];
    expect(conv.messages[1].status).toBe('done');
    expect(conv.activeRunId).toBeNull();
  });

  // 第四轮 P1-2: run.cancelled 是管理员撤销（Hard Kill）的终态。
  // 此前 reducer 没有该分支、finalizeRun 也只认 failed/interrupted，
  // 于是 cancelled 被映射成 done —— 用户看到空白的“成功回答”。
  it('run.cancelled closes the run as cancelled, not done', () => {
    const state = reduce(
      stateWithStream(),
      event('run.cancelled', {
        status: 'cancelled',
        error_code: 'execution_disabled',
        error_message: 'application or runtime binding was disabled before execution',
      }),
    );
    const msg = state.conversations[42].messages[1];
    expect(msg.status).toBe('cancelled');
    expect(msg.error).toBe('应用或运行配置已停用，本次执行已取消。');
    expect(state.conversations[42].activeRunId).toBeNull();
  });

  it('run.cancelled without a kill reason falls back to a generic notice', () => {
    const state = reduce(stateWithStream(), event('run.cancelled', { status: 'cancelled' }));
    expect(state.conversations[42].messages[1].status).toBe('cancelled');
    expect(state.conversations[42].messages[1].error).toBe('执行已取消');
  });

  // 第四轮: run.deferred（Provider 暂停 / 执行检查不可用）是非终态。
  it('run.deferred keeps the run streaming and shows a waiting notice', () => {
    let state = stateWithStream();
    state = reduce(state, event('content.delta', { text: '部分回答' }));
    state = reduce(state, event('run.deferred', { reason: 'provider_disabled' }));
    const conv = state.conversations[42];
    expect(conv.messages[1].status).toBe('streaming');
    expect(conv.activeRunId).toBe(runId);
    expect(conv.messages[1].content).toBe('部分回答');
    expect(conv.messages[1].retryNotice).toBe('服务暂时停用，等待恢复…');
  });

  it('run.deferred for an unavailable gate explains the wait', () => {
    const state = reduce(
      stateWithStream(),
      event('run.deferred', { reason: 'run_gate_unavailable' }),
    );
    expect(state.conversations[42].messages[1].retryNotice)
      .toBe('执行检查暂不可用，正在等待重试…');
  });

  it('run.started clears the deferred/retry notice', () => {
    let state = stateWithStream();
    state = reduce(state, event('run.deferred', { reason: 'provider_disabled' }));
    expect(state.conversations[42].messages[1].retryNotice).toBeTruthy();
    state = reduce(state, event('run.started', {}));
    expect(state.conversations[42].messages[1].retryNotice).toBeUndefined();
  });

  it('legacy run.interrupted (historical replay) renders as failure', () => {
    const state = reduce(
      stateWithStream(),
      event('run.interrupted', { reason: 'lease_expired' }),
    );
    const msg = state.conversations[42].messages[1];
    expect(msg.status).toBe('failed');
    expect(state.conversations[42].activeRunId).toBeNull();
  });

  // 第四轮 P1-2: 事件流可能没送到 run.cancelled（断线/重连），此时
  // finalizeRun 用 GET Run 兜底 —— status=cancelled 绝不能被写成 done。
  describe('finalizeRun (GET Run reconciliation)', () => {
    beforeEach(() => {
      vi.mocked(fetchRunArtifacts).mockResolvedValue([]);
      useRunChatStore.setState({
        ...useRunChatStore.getState(),
        conversations: {
          42: {
            id: 42,
            title: 't',
            messages: [
              { id: 'user-run-1', role: 'user', content: 'hi', created_at: '' },
              {
                id: 'run-run-1', role: 'assistant', content: '',
                created_at: '', runId, status: 'streaming', artifacts: [],
              },
            ],
            activeRunId: runId,
          },
        },
      });
    });

    it('keeps a cancelled run cancelled (never done)', async () => {
      vi.mocked(getRun).mockResolvedValue({
        id: runId,
        conversation: 42,
        status: 'cancelled',
        error_code: 'execution_disabled',
      } as any);
      await finalizeRun(runId);
      const conv = useRunChatStore.getState().conversations[42];
      expect(conv.messages[1].status).toBe('cancelled');
      expect(conv.messages[1].error).toBe('应用或运行配置已停用，本次执行已取消。');
      expect(conv.activeRunId).toBeNull();
    });

    it('maps a failed run to failed and a succeeded run to done', async () => {
      vi.mocked(getRun).mockResolvedValue({
        id: runId, conversation: 42, status: 'failed', error_message: 'boom',
      } as any);
      await finalizeRun(runId);
      expect(useRunChatStore.getState().conversations[42].messages[1].status).toBe('failed');

      vi.mocked(getRun).mockResolvedValue({
        id: runId, conversation: 42, status: 'succeeded',
      } as any);
      await finalizeRun(runId);
      expect(useRunChatStore.getState().conversations[42].messages[1].status).toBe('done');
    });
  });

  it('re-seeds the streaming bubble when a history reload wiped it', () => {
    // Simulate: send created conversation 42, then a detail load replaced
    // the store's copy with only the persisted user message.
    const wiped: RunChatState = {
      ...stateWithStream(),
      conversations: {
        42: {
          id: 42, title: 't',
          messages: [{ id: 'srv-1', role: 'user', content: 'hi', created_at: '' }],
          activeRunId: null,
        },
      },
      activeConversationId: 42,
    };
    const after = reduce(wiped, event('content.delta', { text: '流式内容' }));
    const conv = after.conversations[42];
    const seeded = conv.messages.find((m) => m.id === `run-${runId}`);
    expect(seeded?.content).toBe('流式内容');
    expect(seeded?.status).toBe('streaming');
    expect(conv.activeRunId).toBe(runId);
  });
});
