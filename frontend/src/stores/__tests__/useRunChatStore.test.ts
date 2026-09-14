import { describe, expect, it } from 'vitest';
import { validateAttachment, ATTACHMENT_LIMITS } from '@/services/runApi';
import { applyEvent, useRunChatStore } from '../useRunChatStore';
import type { RunEventRecord } from '@/services/runApi';
import type { RunChatState } from '../useRunChatStore';

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

  it('legacy run.interrupted (historical replay) renders as failure', () => {
    const state = reduce(
      stateWithStream(),
      event('run.interrupted', { reason: 'lease_expired' }),
    );
    const msg = state.conversations[42].messages[1];
    expect(msg.status).toBe('failed');
    expect(state.conversations[42].activeRunId).toBeNull();
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
