import { beforeEach, describe, expect, it, vi } from 'vitest';
import axiosInstance from '@/services/axios';
import * as sseClient from '@/services/sseClient';
import { useConversationStore } from '../useConversationStore';
import type {
  AgentProtocolEvent,
  AgentQuestion,
} from '@/services/agentProtocol';
import type {
  SSEEventHandler,
} from '@/services/sseClient';

const question = (text: string): AgentQuestion => ({
  header: 'Choice',
  question: text,
  kind: 'question',
  options: [
    { label: 'A', value: 'a' },
    { label: 'B', value: 'b' },
  ],
});

describe('useConversationStore.answerQuestion', () => {
  beforeEach(() => {
    useConversationStore.setState({
      error: null,
      pendingQuestion: null,
      agentActivity: null,
    });
    vi.restoreAllMocks();
  });

  it('does not erase a second question that arrives before resume returns', async () => {
    let finishResume: (() => void) | undefined;
    vi.spyOn(sseClient, 'resumeAgent').mockImplementation(
      () => new Promise<void>((resolve) => { finishResume = resolve; }),
    );

    useConversationStore.setState({ pendingQuestion: question('First?') });
    const request = useConversationStore.getState().answerQuestion(
      'conversation-1', { selections: ['a'] },
    );
    expect(useConversationStore.getState().pendingQuestion).toBeNull();

    const second = question('Second?');
    useConversationStore.setState({
      pendingQuestion: second,
      agentActivity: '等待你的回答',
    });
    finishResume?.();
    await request;

    expect(useConversationStore.getState().pendingQuestion).toEqual(second);
    expect(useConversationStore.getState().agentActivity).toBe('等待你的回答');
  });

  it('keeps the latest conversation when detail requests finish out of order', async () => {
    let resolveFirst: ((value: any) => void) | undefined;
    let resolveSecond: ((value: any) => void) | undefined;
    vi.spyOn(axiosInstance, 'get').mockImplementation((url) => {
      if (url === '/conversations/15/') {
        return new Promise((resolve) => { resolveFirst = resolve; });
      }
      return new Promise((resolve) => { resolveSecond = resolve; });
    });

    const first = useConversationStore.getState().fetchConversationDetail('15');
    const second = useConversationStore.getState().fetchConversationDetail('16');
    resolveSecond?.({ id: 16, title: 'Second', messages: [] });
    await second;
    resolveFirst?.({ id: 15, title: 'First', messages: [] });
    await first;

    expect(useConversationStore.getState().currentConversation?.id).toBe('16');
  });

  it('renders content and tool calls in separate ordered message boxes', () => {
    let handlers: SSEEventHandler | undefined;
    vi.spyOn(sseClient, 'streamChat').mockImplementation(
      (_conversationId, _content, value) => {
        handlers = value;
        return new AbortController();
      },
    );
    useConversationStore.setState({
      currentConversation: {
        id: 'conversation-1',
        title: 'Test',
        created_at: '',
        updated_at: '',
        messages: [],
      },
      streamingMessageId: null,
    });

    useConversationStore.getState().sendMessageStream(
      'conversation-1', 'Read the file',
    );
    let sequence = 0;
    const emit = (
      method: string,
      params: AgentProtocolEvent['params'],
    ) => handlers?.onEvent({ sequence: ++sequence, method, params });
    emit('item/started', {
      item: { id: 'reasoning-1', type: 'reasoning', content: [] },
    });
    emit('item/reasoning/textDelta', {
      itemId: 'reasoning-1',
      delta: 'internal reasoning',
      contentIndex: 0,
    });
    const messagesAfterThinking = useConversationStore.getState()
      .currentConversation?.messages;
    expect(messagesAfterThinking?.[messagesAfterThinking.length - 1]?.content)
      .toBe('');

    emit('item/started', {
      item: { id: 'message-1', type: 'agentMessage', text: '' },
    });
    emit('item/agentMessage/delta', {
      itemId: 'message-1',
      delta: 'I will inspect ',
    });
    emit('item/agentMessage/delta', {
      itemId: 'message-1',
      delta: 'the file first.',
    });
    emit('item/started', {
      item: {
        id: 'tool-mixed',
        type: 'dynamicToolCall',
        tool: 'Read',
        arguments: { path: 'demo.txt' },
        status: 'inProgress',
      },
    });
    emit('item/started', {
      item: { id: 'message-2', type: 'agentMessage', text: '' },
    });
    emit('item/agentMessage/delta', {
      itemId: 'message-2',
      delta: 'The file',
    });
    emit('item/completed', {
      item: {
        id: 'message-2',
        type: 'agentMessage',
        text: 'The file contains the expected value.',
      },
    });
    emit('turn/completed', {
      turn: { id: 'turn-1', status: 'completed', items: [] },
    });

    const messages = useConversationStore.getState().currentConversation?.messages;
    const assistantMessages = messages?.filter((message) => message.role === 'assistant');
    expect(assistantMessages).toHaveLength(3);
    expect(assistantMessages?.[0].content).toBe('I will inspect the file first.');
    expect(assistantMessages?.[0].metadata?.agent?.tool_calls).toBeUndefined();
    expect(assistantMessages?.[1].content).toBe('');
    expect(assistantMessages?.[1].metadata?.agent?.tool_calls).toHaveLength(1);
    expect(assistantMessages?.[2].content).toBe('The file contains the expected value.');
    expect(assistantMessages?.[2].metadata?.agent?.tool_calls).toBeUndefined();
  });
});
