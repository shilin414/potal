/** Codex App Server-shaped protocol types and reducer used by every agent. */

export interface AgentOption {
  label: string;
  value: string;
  description?: string;
}

export interface AgentQuestion {
  header: string;
  question: string;
  kind: 'question' | 'permission';
  options: AgentOption[];
}

export interface AgentToolCall {
  id: string;
  name: string;
  input: string;
  result: string;
  status: string;
  error_message: string;
}

export interface AgentThreadItem {
  id: string;
  type: string;
  status?: string;
  text?: string;
  content?: string[];
  summary?: string[];
  command?: string;
  cwd?: string;
  aggregatedOutput?: string;
  tool?: string;
  namespace?: string;
  arguments?: unknown;
  result?: unknown;
  error?: string | null;
  success?: boolean | null;
  query?: string;
  results?: unknown;
  changes?: unknown[];
  [key: string]: unknown;
}

export interface AgentTurnSnapshot {
  id: string;
  status: string;
  items?: AgentThreadItem[];
  usage?: Record<string, number>;
  error?: { code?: string; message?: string } | null;
}

export interface AgentProtocolEvent {
  sequence: number;
  method: string;
  params: {
    threadId?: string;
    turnId?: string;
    itemId?: string;
    requestId?: string;
    delta?: string;
    item?: AgentThreadItem;
    turn?: AgentTurnSnapshot;
    question?: Partial<AgentQuestion>;
    skills?: string[];
    missingSkills?: string[];
    [key: string]: unknown;
  };
  emittedAtMs?: number;
}

export interface AgentPendingRequest {
  requestId: string;
  method: string;
  question: AgentQuestion;
  status: 'pending' | 'resolved';
}

export interface AgentRuntimeState {
  itemsById: Record<string, AgentThreadItem>;
  itemOrder: string[];
  activeTurnId: string | null;
  turnStatus: string | null;
  pendingRequests: Record<string, AgentPendingRequest>;
  loadedSkills: string[];
  missingSkills: string[];
  lastSequence: number;
  turnError: { code?: string; message?: string } | null;
}

export const initialAgentRuntimeState = (): AgentRuntimeState => ({
  itemsById: {},
  itemOrder: [],
  activeTurnId: null,
  turnStatus: null,
  pendingRequests: {},
  loadedSkills: [],
  missingSkills: [],
  lastSequence: 0,
  turnError: null,
});

const normalizeQuestion = (
  value: Partial<AgentQuestion> | undefined,
  kind: AgentQuestion['kind'],
): AgentQuestion => ({
  header: value?.header || '',
  question: value?.question || '',
  kind,
  options: Array.isArray(value?.options) ? value.options : [],
});

const upsertItem = (
  state: AgentRuntimeState,
  item: AgentThreadItem,
): Pick<AgentRuntimeState, 'itemsById' | 'itemOrder'> => ({
  itemsById: {
    ...state.itemsById,
    [item.id]: { ...state.itemsById[item.id], ...item },
  },
  itemOrder: state.itemOrder.includes(item.id)
    ? state.itemOrder
    : [...state.itemOrder, item.id],
});

/** Reduce one ordered server notification into the current turn projection. */
export function reduceAgentEvent(
  state: AgentRuntimeState,
  event: AgentProtocolEvent,
): AgentRuntimeState {
  // Sequences are scoped to one SSE turn. Ignore duplicate/replayed frames
  // after the first frame has established the current turn.
  if (state.lastSequence > 0 && event.sequence <= state.lastSequence) return state;

  const next: AgentRuntimeState = { ...state, lastSequence: event.sequence };
  const { method, params } = event;

  if (method === 'turn/started' && params.turn) {
    return {
      ...initialAgentRuntimeState(),
      activeTurnId: params.turn.id,
      turnStatus: params.turn.status,
      lastSequence: event.sequence,
    };
  }

  if ((method === 'item/started' || method === 'item/completed') && params.item) {
    return { ...next, ...upsertItem(state, params.item) };
  }

  if (method === 'item/agentMessage/delta' && params.itemId) {
    const current = state.itemsById[params.itemId] || {
      id: params.itemId,
      type: 'agentMessage',
      text: '',
    };
    const item = { ...current, text: `${current.text || ''}${params.delta || ''}` };
    return { ...next, ...upsertItem(state, item) };
  }

  if (method === 'item/reasoning/textDelta' && params.itemId) {
    const current = state.itemsById[params.itemId] || {
      id: params.itemId,
      type: 'reasoning',
      content: [],
    };
    const content = Array.isArray(current.content) ? [...current.content] : [];
    const contentIndex = typeof params.contentIndex === 'number'
      ? params.contentIndex
      : 0;
    content[contentIndex] = `${content[contentIndex] || ''}${params.delta || ''}`;
    return { ...next, ...upsertItem(state, { ...current, content }) };
  }

  if (method === 'skills/changed') {
    return {
      ...next,
      loadedSkills: Array.isArray(params.skills) ? params.skills : [],
      missingSkills: Array.isArray(params.missingSkills) ? params.missingSkills : [],
    };
  }

  if (
    method === 'tool/requestUserInput'
    || method === 'item/commandExecution/requestApproval'
  ) {
    const requestId = params.requestId || `request-${event.sequence}`;
    const kind = method === 'tool/requestUserInput' ? 'question' : 'permission';
    return {
      ...next,
      pendingRequests: {
        ...state.pendingRequests,
        [requestId]: {
          requestId,
          method,
          question: normalizeQuestion(params.question, kind),
          status: 'pending',
        },
      },
    };
  }

  if (method === 'serverRequest/resolved' && params.requestId) {
    const request = state.pendingRequests[params.requestId];
    if (!request) return next;
    return {
      ...next,
      pendingRequests: {
        ...state.pendingRequests,
        [params.requestId]: { ...request, status: 'resolved' },
      },
    };
  }

  if (method === 'turn/completed' && params.turn) {
    let itemsById = state.itemsById;
    let itemOrder = state.itemOrder;
    for (const item of params.turn.items || []) {
      const projected = upsertItem({ ...state, itemsById, itemOrder }, item);
      itemsById = projected.itemsById;
      itemOrder = projected.itemOrder;
    }
    return {
      ...next,
      itemsById,
      itemOrder,
      activeTurnId: null,
      turnStatus: params.turn.status,
      turnError: params.turn.error || null,
    };
  }

  return next;
}

const stringify = (value: unknown): string => {
  if (value == null) return '';
  if (typeof value === 'string') return value;
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
};

/** Adapt a canonical tool-like item to the existing compact tool card UI. */
export function itemToToolCall(item: AgentThreadItem): AgentToolCall | null {
  if (item.type === 'commandExecution') {
    return {
      id: item.id,
      name: 'command',
      input: stringify({ command: item.command || '', cwd: item.cwd || '' }),
      result: stringify(item.aggregatedOutput),
      status: item.status || 'running',
      error_message: stringify(item.error),
    };
  }
  if (item.type === 'fileChange') {
    return {
      id: item.id,
      name: 'apply_patch',
      input: stringify(item.changes || []),
      result: '',
      status: item.status || 'running',
      error_message: stringify(item.error),
    };
  }
  if (item.type === 'webSearch') {
    return {
      id: item.id,
      name: 'web_search',
      input: stringify({ query: item.query || '' }),
      result: stringify(item.results),
      status: item.results == null ? 'running' : 'completed',
      error_message: stringify(item.error),
    };
  }
  if (item.type === 'dynamicToolCall') {
    return {
      id: item.id,
      name: item.tool || 'tool',
      input: stringify(item.arguments),
      result: stringify(item.result),
      status: item.status || 'running',
      error_message: stringify(item.error),
    };
  }
  return null;
}
