/**
 * 第九轮 P0-1: the SEND ACTION owns its idempotency identity.
 *
 * The bug this pins is a UI-visible duplicate: when POST /v2/runs commits but
 * the response is lost, the user sees 发送失败 and sends again. With a fresh
 * token the second request is a legitimate new turn — a second user message
 * and a second provider chat for ONE message the user typed once.
 *
 * So the rule under test is about user intent, not HTTP attempts:
 *
 *   same payload retried → SAME client_request_id (a replay)
 *   edited payload       → NEW client_request_id (a new turn)
 *   replayed response    → no duplicated messages in the store
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/services/runApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/runApi')>();
  return {
    ...actual,
    createRun: vi.fn(),
    getRun: vi.fn(),
    fetchRunArtifacts: vi.fn(async () => []),
  };
});

// The store opens the SSE stream right after a successful send; the transport
// itself is covered by runStream.test.ts.
vi.mock('@/services/runStream', () => ({
  openRunStream: vi.fn(() => ({ close: vi.fn() })),
}));

import { createRun } from '@/services/runApi';
import type { RunRecord } from '@/services/runApi';
import { useRunChatStore } from '../useRunChatStore';

function runRecord(id: string, conversation: number, extra: Partial<RunRecord> = {}): RunRecord {
  return {
    id,
    application: 1,
    conversation,
    provider: 'feishu_aily',
    runtime_type: 'agent',
    status: 'queued',
    created_at: new Date().toISOString(),
    ...extra,
  };
}

/** The client_request_id of the Nth createRun call. */
function sentRequestId(call: number): string | undefined {
  const args = vi.mocked(createRun).mock.calls[call];
  return (args[2] as { clientRequestId?: string } | undefined)?.clientRequestId;
}

beforeEach(() => {
  vi.mocked(createRun).mockReset();
  useRunChatStore.setState({
    conversations: {},
    activeConversationId: null,
    isLoading: false,
    error: null,
    lastConversationId: null,
  });
});

describe('sendMessage idempotency identity', () => {
  it('always attaches a client_request_id', async () => {
    vi.mocked(createRun).mockResolvedValue(runRecord('run-a', 11));

    await useRunChatStore.getState().sendMessage({
      applicationId: 1,
      conversationId: null,
      content: 'identity check',
    });

    const id = sentRequestId(0);
    expect(typeof id).toBe('string');
    expect(id!.length).toBeGreaterThan(0);
    expect(id!.length).toBeLessThanOrEqual(64); // the backend rejects longer
  });

  it('reuses the SAME id when an identical send is retried after a failure', async () => {
    vi.mocked(createRun)
      .mockRejectedValueOnce(new Error('gateway timeout'))
      .mockResolvedValueOnce(runRecord('run-retry', 12));

    const params = { applicationId: 1, conversationId: null, content: 'retry me' };
    const first = await useRunChatStore.getState().sendMessage(params);
    expect(first).toBeNull(); // the failure is reported to the UI

    const second = await useRunChatStore.getState().sendMessage(params);
    expect(second).toBe(12);

    expect(sentRequestId(1)).toBe(sentRequestId(0));
  });

  it('reuses the SAME id when only the attachment ORDER differs', async () => {
    vi.mocked(createRun)
      .mockRejectedValueOnce(new Error('boom'))
      .mockResolvedValueOnce(runRecord('run-att', 13));

    await useRunChatStore.getState().sendMessage({
      applicationId: 1,
      conversationId: null,
      content: 'with files',
      attachments: [{ id: 'a1', name: 'a.png' }, { id: 'a2', name: 'b.png' }],
    });
    await useRunChatStore.getState().sendMessage({
      applicationId: 1,
      conversationId: null,
      content: 'with files',
      attachments: [{ id: 'a2', name: 'b.png' }, { id: 'a1', name: 'a.png' }],
    });

    expect(sentRequestId(1)).toBe(sentRequestId(0));
  });

  it('uses a NEW id when the user edits the message and sends again', async () => {
    vi.mocked(createRun)
      .mockRejectedValueOnce(new Error('boom'))
      .mockResolvedValueOnce(runRecord('run-edited', 14));

    await useRunChatStore.getState().sendMessage({
      applicationId: 1, conversationId: null, content: 'first wording',
    });
    await useRunChatStore.getState().sendMessage({
      applicationId: 1, conversationId: null, content: 'second wording',
    });

    expect(sentRequestId(1)).not.toBe(sentRequestId(0));
  });

  it('uses a NEW id after a successful send (a fresh action is a fresh turn)', async () => {
    vi.mocked(createRun)
      .mockResolvedValueOnce(runRecord('run-one', 15))
      .mockResolvedValueOnce(runRecord('run-two', 15));

    await useRunChatStore.getState().sendMessage({
      applicationId: 1, conversationId: 15, content: 'message one',
    });
    await useRunChatStore.getState().sendMessage({
      applicationId: 1, conversationId: 15, content: 'message two',
    });

    expect(sentRequestId(1)).not.toBe(sentRequestId(0));
  });
});

describe('sendMessage on an idempotent replay', () => {
  it('does not insert the turn twice when a retry returns the original run', async () => {
    // The documented retry shape: the CALLER owns the identity (the
    // `clientRequestId` parameter) and re-sends after a transport failure.
    // The server answers the second call from the reservation — same run id,
    // flagged as a replay — and the store already rendered that turn from
    // the first response, so inserting again would show it twice.
    vi.mocked(createRun)
      .mockResolvedValueOnce(runRecord('run-replayed', 16))
      .mockResolvedValueOnce(runRecord('run-replayed', 16, { idempotency_replayed: true }));

    const params = {
      applicationId: 1,
      conversationId: null,
      content: 'replayed',
      clientRequestId: 'caller-owned-id',
    };
    await useRunChatStore.getState().sendMessage(params);
    await useRunChatStore.getState().sendMessage(params);

    // The explicit identity is forwarded verbatim on both attempts — that is
    // what makes the second one a replay instead of a second turn.
    expect(sentRequestId(0)).toBe('caller-owned-id');
    expect(sentRequestId(1)).toBe('caller-owned-id');

    const messages = useRunChatStore.getState().conversations[16].messages;
    expect(messages.map((m) => m.id)).toEqual(['user-run-replayed', 'run-run-replayed']);
    expect(messages.filter((m) => m.role === 'user')).toHaveLength(1);
  });

  it('keeps the conversation usable: the run stays active after a replay', async () => {
    vi.mocked(createRun).mockResolvedValue(
      runRecord('run-active', 17, { idempotency_replayed: true }),
    );

    await useRunChatStore.getState().sendMessage({
      applicationId: 1, conversationId: null, content: 'active after replay',
    });

    expect(useRunChatStore.getState().conversations[17].activeRunId).toBe('run-active');
    expect(useRunChatStore.getState().activeConversationId).toBe(17);
  });
});
