import { describe, expect, it } from 'vitest';
import {
  initialAgentRuntimeState,
  itemToToolCall,
  reduceAgentEvent,
  type AgentProtocolEvent,
} from '../agentProtocol';

const event = (
  sequence: number,
  method: string,
  params: AgentProtocolEvent['params'],
): AgentProtocolEvent => ({ sequence, method, params });

describe('agentProtocol', () => {
  it('projects turn, item, and delta notifications into runtime state', () => {
    let state = initialAgentRuntimeState();
    state = reduceAgentEvent(state, event(1, 'turn/started', {
      turn: { id: 'turn-1', status: 'inProgress', items: [] },
    }));
    state = reduceAgentEvent(state, event(2, 'item/started', {
      item: { id: 'message-1', type: 'agentMessage', text: '' },
    }));
    state = reduceAgentEvent(state, event(3, 'item/agentMessage/delta', {
      itemId: 'message-1',
      delta: 'Hello',
    }));

    expect(state.activeTurnId).toBe('turn-1');
    expect(state.itemOrder).toEqual(['message-1']);
    expect(state.itemsById['message-1'].text).toBe('Hello');

    state = reduceAgentEvent(state, event(4, 'turn/completed', {
      turn: {
        id: 'turn-1',
        status: 'completed',
        items: [{ id: 'message-1', type: 'agentMessage', text: 'Hello' }],
      },
    }));
    expect(state.activeTurnId).toBeNull();
    expect(state.turnStatus).toBe('completed');
  });

  it('tracks requests and ignores duplicate sequence frames', () => {
    const initial = reduceAgentEvent(initialAgentRuntimeState(), event(
      1,
      'tool/requestUserInput',
      {
        requestId: 'request-1',
        question: { question: 'Continue?', options: [] },
      },
    ));
    const duplicate = reduceAgentEvent(initial, event(1, 'skills/changed', {
      skills: ['should-not-apply'],
    }));

    expect(initial.pendingRequests['request-1'].question.kind).toBe('question');
    expect(duplicate).toBe(initial);
  });

  it('adapts canonical command items for the existing tool card', () => {
    expect(itemToToolCall({
      id: 'command-1',
      type: 'commandExecution',
      command: 'pwd',
      cwd: 'D:/workspace',
      aggregatedOutput: 'D:/workspace',
      status: 'completed',
    })).toMatchObject({
      id: 'command-1',
      name: 'command',
      result: 'D:/workspace',
      status: 'completed',
    });
  });
});
