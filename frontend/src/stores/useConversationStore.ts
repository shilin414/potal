import { create } from 'zustand';
import {
  captureSessionGeneration,
  registerSessionReset,
  sessionStillCurrent,
} from '@/stores/resetSessionState';
import axiosInstance from '@/services/axios';
import {
  cancelAgentTurn,
  resumeAgent,
  streamChat,
  type ChatRunOptions,
} from '@/services/sseClient';
import {
  initialAgentRuntimeState,
  itemToToolCall,
  reduceAgentEvent,
  type AgentProtocolEvent,
  type AgentQuestion,
  type AgentRuntimeState,
  type AgentToolCall,
} from '@/services/agentProtocol';

interface Message {
  id: string;
  role: 'user' | 'assistant' | 'system';
  content: string;
  created_at: string;
  metadata?: any;
}

export interface Conversation {
  id: string;
  title: string;
  agent?: any;
  /** Project (workspace) id this conversation belongs to, if scoped. */
  project?: number | null;
  /** Template process id within the workspace, e.g. "cover". */
  process_id?: string;
  created_at: string;
  updated_at: string;
  last_message?: {
    role: string;
    content: string;
    created_at: string;
  };
  message_count?: number;
}

export interface ConversationDetail extends Conversation {
  messages: Message[];
}

/** Normalize any conversation payload into a clean array of valid entries.
 *  Handles paginated `{ results }` shapes and drops entries missing an id, so
 *  stale persisted state or a malformed response can never crash the UI. */
const normalizeConversations = (raw: any): Conversation[] => {
  if (!raw) return [];
  const list: any[] = Array.isArray(raw) ? raw : Array.isArray(raw?.results) ? raw.results : [];
  return list.filter((c: any) => c && c.id != null) as Conversation[];
};

let latestConversationDetailRequest = 0;

/**
 * Every legacy conversation stream this session opened and that has not
 * finished yet (四次复审 P0-R3).
 *
 * `clearAll()` aborts them all. Aborting is not sufficient on its own —
 * a callback can already be queued when the AbortController fires — so each
 * callback ALSO checks the global session epoch before touching state.
 */
const activeConversationStreams = new Set<AbortController>();

function closeAllConversationStreams(): void {
  for (const controller of activeConversationStreams) {
    try {
      controller.abort();
    } catch {
      // An already-aborted controller must not abort the rest of the sweep.
    }
  }
  activeConversationStreams.clear();
}

interface ConversationState extends AgentRuntimeState {
  conversations: Conversation[];
  currentConversation: ConversationDetail | null;
  isLoading: boolean;
  error: string | null;
  streamingMessageId: string | null;
  pendingQuestion: AgentQuestion | null;
  agentActivity: string | null;

  // Actions
  fetchConversations: () => Promise<void>;
  fetchProjectConversations: (projectId: number | string) => Promise<Conversation[]>;
  fetchConversationDetail: (id: string) => Promise<void>;
  createConversation: (
    title?: string,
    agentId?: number,
    projectId?: number,
    processId?: string,
    context?: {
      applicationId?: number;
      workflowStepRunId?: string;
      agentId?: number;
      skillIds?: string[];
      workingDirectory?: string;
    },
  ) => Promise<Conversation>;
  sendMessage: (conversationId: string, content: string) => Promise<{ user_message: Message; assistant_message: Message }>;
  sendMessageStream: (
    conversationId: string,
    content: string,
    options?: ChatRunOptions,
  ) => AbortController;
  appendStreamContent: (content: string) => void;
  replaceStreamContent: (content: string) => void;
  recordStreamToolCall: (toolCall: AgentToolCall) => void;
  recordLoadedSkills: (skills: string[]) => void;
  finalizeStreamMessage: (messageId: string) => void;
  answerQuestion: (
    conversationId: string,
    answer: { text?: string; selections?: string[] },
  ) => Promise<void>;
  cancelTurn: (conversationId: string) => Promise<void>;
  clearConversation: (conversationId: string) => Promise<void>;
  deleteConversation: (conversationId: string) => Promise<void>;
  setCurrentConversation: (conversation: ConversationDetail | null) => void;
  clearError: () => void;
  /**
   * Forget every transcript (四次复审 P0-R2). This store is deliberately NOT
   * persisted any more: its facts live behind the backend, and the old
   * `conversation-storage` copy carried one user's full transcript into the
   * next browser session. `clearAll` still drops everything on a user switch.
   */
  clearAll: () => void;
}

export const useConversationStore = create<ConversationState>()((set, get) => ({
      conversations: [],
      currentConversation: null,
      isLoading: false,
      error: null,
      streamingMessageId: null,
      pendingQuestion: null,
      agentActivity: null,
      ...initialAgentRuntimeState(),

      clearAll: () => {
        // Invalidate an in-flight detail response AND close every live
        // stream BEFORE clearing state: the epoch bump in
        // resetSessionScopedState() happens before this callback runs, and
        // the request id covers the same-session "newer detail" race.
        latestConversationDetailRequest += 1;
        closeAllConversationStreams();
        set({
          conversations: [],
          currentConversation: null,
          isLoading: false,
          error: null,
          streamingMessageId: null,
          pendingQuestion: null,
          agentActivity: null,
          ...initialAgentRuntimeState(),
        });
      },

      fetchConversations: async () => {
        const generation = captureSessionGeneration();
        set({ isLoading: true, error: null });
        try {
          const response = await axiosInstance.get('/conversations/') as any;
          if (!sessionStillCurrent(generation)) return;
          set({ conversations: normalizeConversations(response), isLoading: false });
        } catch (error: any) {
          if (!sessionStillCurrent(generation)) return;
          set({
            error: error.response?.data?.detail || '获取对话列表失败',
            isLoading: false,
          });
          throw error;
        }
      },

      fetchConversationDetail: async (id: string) => {
        const generation = captureSessionGeneration();
        const requestId = ++latestConversationDetailRequest;
        set({ isLoading: true, error: null });
        try {
          const response = await axiosInstance.get(`/conversations/${id}/`) as any;
          if (
            sessionStillCurrent(generation)
            && requestId === latestConversationDetailRequest
          ) {
            set({
              currentConversation: { ...response, id: String(response.id) },
              isLoading: false,
            });
          }
        } catch (error: any) {
          if (!sessionStillCurrent(generation)) return;
          if (requestId === latestConversationDetailRequest) {
            set({
              error: error.response?.data?.detail || '获取对话详情失败',
              isLoading: false,
            });
          }
          throw error;
        }
      },

      fetchProjectConversations: async (projectId: number | string) => {
        const generation = captureSessionGeneration();
        try {
          const response = await axiosInstance.get('/conversations/', {
            params: { project_id: projectId },
          }) as any;
          if (!sessionStillCurrent(generation)) return [];
          const list = Array.isArray(response) ? response : response.results ?? [];
          return list as Conversation[];
        } catch (error: any) {
          if (!sessionStillCurrent(generation)) return [];
          console.error('Failed to load project conversations:', error);
          return [];
        }
      },

      createConversation: async (title?: string, agentId?: number, projectId?: number, processId?: string, context = {}) => {
        const generation = captureSessionGeneration();
        set({ isLoading: true, error: null });
        try {
          const payload: Record<string, unknown> = { title };
          const effectiveAgentId = agentId ?? context.agentId;
          if (effectiveAgentId !== undefined) payload.agent_id = effectiveAgentId;
          if (projectId !== undefined) payload.project_id = projectId;
          if (processId !== undefined) payload.process_id = processId;
          if (context.applicationId) payload.application_id = context.applicationId;
          if (context.workflowStepRunId) payload.workflow_step_run_id = context.workflowStepRunId;
          if (context.skillIds?.length) payload.skill_ids = context.skillIds;
          if (context.workingDirectory) payload.working_directory = context.workingDirectory;
          const response = await axiosInstance.post('/conversations/', payload) as any;
          if (!sessionStillCurrent(generation)) {
            throw new Error('session changed');
          }
          // Workspace-scoped conversations are not part of the home chat list.
          if (!projectId) {
            const { conversations } = get();
            set({ conversations: [response, ...conversations] });
          }
          set({ isLoading: false });
          return response;
        } catch (error: any) {
          if (!sessionStillCurrent(generation)) throw error;
          set({
            error: error.response?.data?.detail || '创建对话失败',
            isLoading: false,
          });
          throw error;
        }
      },

      sendMessage: async (conversationId: string, content: string) => {
        const generation = captureSessionGeneration();
        set({ isLoading: true, error: null });
        try {
          const response = await axiosInstance.post(
            `/conversations/${conversationId}/send_message/`,
            { content }
          ) as any;
          if (!sessionStillCurrent(generation)) {
            throw new Error('session changed');
          }

          // Update current conversation if it's the active one
          const { currentConversation } = get();
          if (currentConversation && currentConversation.id === conversationId) {
            set({
              currentConversation: {
                ...currentConversation,
                messages: [
                  ...currentConversation.messages,
                  response.user_message,
                  response.assistant_message,
                ],
              },
              isLoading: false,
            });
          } else {
            set({ isLoading: false });
          }

          // Refresh conversation list to update last_message
          get().fetchConversations();

          return response;
        } catch (error: any) {
          if (!sessionStillCurrent(generation)) throw error;
          set({
            error: error.response?.data?.detail || '发送消息失败',
            isLoading: false,
          });
          throw error;
        }
      },

      clearConversation: async (conversationId: string) => {
        const generation = captureSessionGeneration();
        set({ isLoading: true, error: null });
        try {
          await axiosInstance.delete(`/conversations/${conversationId}/clear/`);
          if (!sessionStillCurrent(generation)) return;

          // Update current conversation if it's the active one
          const { currentConversation } = get();
          if (currentConversation && currentConversation.id === conversationId) {
            set({
              currentConversation: {
                ...currentConversation,
                messages: [],
              },
              isLoading: false,
            });
          } else {
            set({ isLoading: false });
          }
        } catch (error: any) {
          if (!sessionStillCurrent(generation)) throw error;
          set({
            error: error.response?.data?.detail || '清空对话失败',
            isLoading: false,
          });
          throw error;
        }
      },

      deleteConversation: async (conversationId: string) => {
        const generation = captureSessionGeneration();
        set({ isLoading: true, error: null });
        try {
          await axiosInstance.delete(`/conversations/${conversationId}/delete_conversation/`);
          if (!sessionStillCurrent(generation)) return;

          // Remove from conversations list
          const { conversations, currentConversation } = get();
          set({
            conversations: conversations.filter(c => c.id !== conversationId),
            currentConversation: currentConversation?.id === conversationId ? null : currentConversation,
            isLoading: false,
          });
        } catch (error: any) {
          if (!sessionStillCurrent(generation)) throw error;
          set({
            error: error.response?.data?.detail || '删除对话失败',
            isLoading: false,
          });
          throw error;
        }
      },

      setCurrentConversation: (conversation: ConversationDetail | null) => {
        set({ currentConversation: conversation });
      },

      clearError: () => {
        set({ error: null });
      },

      sendMessageStream: (conversationId: string, content: string, options = {}) => {
        const generation = captureSessionGeneration();
        const { currentConversation } = get();

        const userMsg: Message = {
          id: `temp-user-${Date.now()}`,
          role: 'user',
          content,
          created_at: new Date().toISOString(),
          metadata: {
            composer: {
              skills: options.skills || [],
              agent_id: options.agentId ?? null,
              permission_mode: options.permissionMode || 'default',
            },
          },
        };

        const assistantMsgId = `temp-assistant-${Date.now()}`;
        const assistantMsg: Message = {
          id: assistantMsgId,
          role: 'assistant',
          content: '',
          created_at: new Date().toISOString(),
        };
        const itemMessageIds = new Map<string, string>();
        let firstAssistantMessageAvailable = true;
        let streamSegmentSequence = 0;

        const activateContentItem = (itemId: string) => {
          if (!sessionStillCurrent(generation)) return '';
          const existingId = itemMessageIds.get(itemId);
          if (existingId) {
            set({ streamingMessageId: existingId });
            return existingId;
          }
          const messageId = firstAssistantMessageAvailable
            ? assistantMsgId
            : `${assistantMsgId}-content-${++streamSegmentSequence}`;
          firstAssistantMessageAvailable = false;
          itemMessageIds.set(itemId, messageId);
          if (messageId === assistantMsgId) {
            set({ streamingMessageId: messageId });
            return messageId;
          }
          const state = get();
          if (!state.currentConversation) return messageId;
          set({
            streamingMessageId: messageId,
            currentConversation: {
              ...state.currentConversation,
              messages: [
                ...state.currentConversation.messages,
                {
                  id: messageId,
                  role: 'assistant',
                  content: '',
                  created_at: new Date().toISOString(),
                },
              ],
            },
          });
          return messageId;
        };

        set({
          error: null,
          streamingMessageId: assistantMsgId,
          pendingQuestion: null,
          agentActivity: '正在启动 Agent Engine…',
          ...initialAgentRuntimeState(),
          currentConversation: currentConversation
            ? {
                ...currentConversation,
                messages: [...currentConversation.messages, userMsg, assistantMsg],
              }
            : null,
        });

        const controller = streamChat(conversationId, content, {
          onEvent: (event: AgentProtocolEvent) => {
            if (!sessionStillCurrent(generation)) return;
            const state = get();
            const runtime: AgentRuntimeState = {
              itemsById: state.itemsById,
              itemOrder: state.itemOrder,
              activeTurnId: state.activeTurnId,
              turnStatus: state.turnStatus,
              pendingRequests: state.pendingRequests,
              loadedSkills: state.loadedSkills,
              missingSkills: state.missingSkills,
              lastSequence: state.lastSequence,
              turnError: state.turnError,
            };
            const nextRuntime = reduceAgentEvent(runtime, event);
            set(nextRuntime);

            const { method, params } = event;
            const item = params.item
              || (params.itemId ? nextRuntime.itemsById[params.itemId] : undefined);

            if (method === 'turn/started') {
              set({ agentActivity: 'Agent 正在运行…' });
              return;
            }
            if (method === 'skills/changed') {
              get().recordLoadedSkills(nextRuntime.loadedSkills);
              set({
                agentActivity: nextRuntime.loadedSkills.length
                  ? `已加载技能：${nextRuntime.loadedSkills.join('、')}`
                  : '未加载到所选技能',
              });
              return;
            }
            if (method === 'item/reasoning/textDelta') {
              set({ agentActivity: '正在思考…' });
              return;
            }
            if (method === 'item/agentMessage/delta' && params.itemId) {
              activateContentItem(params.itemId);
              get().appendStreamContent(params.delta || '');
              set({ agentActivity: '正在生成…' });
              return;
            }
            if ((method === 'item/started' || method === 'item/completed') && item) {
              if (item.type === 'agentMessage') {
                activateContentItem(item.id);
                if (method === 'item/completed') {
                  get().replaceStreamContent(item.text || '');
                }
                return;
              }
              const toolCall = itemToToolCall(item);
              if (toolCall) {
                // A tool occupies its own message segment. Any later assistant
                // item must be rendered in a fresh bubble.
                firstAssistantMessageAvailable = false;
                get().recordStreamToolCall(toolCall);
                set({
                  agentActivity: method === 'item/started'
                    ? `正在调用工具：${toolCall.name}`
                    : `工具已完成：${toolCall.name}`,
                });
              }
              return;
            }
            if (
              method === 'tool/requestUserInput'
              || method === 'item/commandExecution/requestApproval'
            ) {
              const requestId = params.requestId || `request-${event.sequence}`;
              set({
                pendingQuestion: nextRuntime.pendingRequests[requestId]?.question || null,
                agentActivity: '等待你的回答',
              });
              return;
            }
            if (method === 'serverRequest/resolved') {
              set({ pendingQuestion: null, agentActivity: 'Agent 已继续运行…' });
              return;
            }
            if (method === 'turn/completed') {
              const status = params.turn?.status;
              if (status === 'failed') {
                set({
                  error: params.turn?.error?.message || 'Agent Engine 执行失败',
                  streamingMessageId: null,
                  pendingQuestion: null,
                  agentActivity: null,
                });
              } else if (status === 'interrupted') {
                const latest = get();
                set({
                  streamingMessageId: null,
                  pendingQuestion: null,
                  agentActivity: null,
                  currentConversation: latest.currentConversation
                    ? {
                        ...latest.currentConversation,
                        messages: latest.currentConversation.messages.filter(
                          (message) => message.id !== latest.streamingMessageId || Boolean(message.content),
                        ),
                      }
                    : null,
                });
              } else {
                set({ pendingQuestion: null, agentActivity: '正在保存结果…' });
              }
            }
          },
          onDone: (event) => {
            if (!sessionStillCurrent(generation)) return;
            get().finalizeStreamMessage(event.message_id || assistantMsgId);
            set({ pendingQuestion: null, agentActivity: null });
            void get().fetchConversationDetail(conversationId).catch(() => undefined);
            get().fetchConversations();
          },
          onError: (err) => {
            if (!sessionStillCurrent(generation)) return;
            set({
              error: `流式响应错误: ${err.message}`,
              streamingMessageId: null,
              pendingQuestion: null,
              agentActivity: null,
            });
          },
        }, options);

        activeConversationStreams.add(controller);
        // streamChat returns a plain AbortController — wrap the abort call so
        // a finished stream removes itself from the registry without
        // affecting the controller's identity.
        const originalAbort = controller.abort.bind(controller);
        controller.abort = () => {
          activeConversationStreams.delete(controller);
          originalAbort();
        };

        return controller;
      },

      appendStreamContent: (content: string) => {
        const { currentConversation, streamingMessageId } = get();
        if (!currentConversation || !streamingMessageId) return;

        set({
          currentConversation: {
            ...currentConversation,
            messages: currentConversation.messages.map((msg) =>
              msg.id === streamingMessageId
                ? { ...msg, content: msg.content + content }
                : msg
            ),
          },
        });
      },

      replaceStreamContent: (content: string) => {
        const { currentConversation, streamingMessageId } = get();
        if (!currentConversation || !streamingMessageId) return;
        set({
          currentConversation: {
            ...currentConversation,
            messages: currentConversation.messages.map((message) =>
              message.id === streamingMessageId ? { ...message, content } : message
            ),
          },
        });
      },

      recordStreamToolCall: (toolCall: AgentToolCall) => {
        const { currentConversation, streamingMessageId } = get();
        if (!currentConversation || !streamingMessageId) return;
        const matchesTool = (item: AgentToolCall) =>
          toolCall.id ? item.id === toolCall.id : item.name === toolCall.name;
        const existingMessage = currentConversation.messages.find((message) => {
          const calls = message.metadata?.agent?.tool_calls
            || message.metadata?.graphflow?.tool_calls;
          return Array.isArray(calls) && calls.some(matchesTool);
        });
        const currentMessage = currentConversation.messages.find(
          (message) => message.id === streamingMessageId,
        );
        const canReuseCurrent = Boolean(
          currentMessage
          && !currentMessage.content
          && !(currentMessage.metadata?.agent?.tool_calls?.length)
          && !(currentMessage.metadata?.graphflow?.tool_calls?.length),
        );
        const toolMessageId = existingMessage?.id
          || (canReuseCurrent
            ? streamingMessageId
            : `temp-tool-${Date.now()}-${currentConversation.messages.length}`);
        const messages = existingMessage || canReuseCurrent
          ? currentConversation.messages
          : [
              ...currentConversation.messages,
              {
                id: toolMessageId,
                role: 'assistant' as const,
                content: '',
                created_at: new Date().toISOString(),
              },
            ];
        set({
          streamingMessageId: toolMessageId,
          currentConversation: {
            ...currentConversation,
            messages: messages.map((message) => {
              if (message.id !== toolMessageId) return message;
              const agent = message.metadata?.agent || {};
              const legacy = message.metadata?.graphflow || {};
              const sourceCalls = agent.tool_calls || legacy.tool_calls;
              const existing: AgentToolCall[] = Array.isArray(sourceCalls)
                ? sourceCalls
                : [];
              const index = existing.findIndex(matchesTool);
              const toolCalls = [...existing];
              if (index >= 0) {
                toolCalls[index] = { ...toolCalls[index], ...toolCall };
              } else {
                toolCalls.push(toolCall);
              }
              return {
                ...message,
                metadata: {
                  ...message.metadata,
                  agent: { ...agent, tool_calls: toolCalls },
                },
              };
            }),
          },
        });
      },

      recordLoadedSkills: (skills: string[]) => {
        const { currentConversation, streamingMessageId } = get();
        if (!currentConversation || !streamingMessageId) return;
        set({
          currentConversation: {
            ...currentConversation,
            messages: currentConversation.messages.map((message) =>
              message.id === streamingMessageId
                ? {
                    ...message,
                    metadata: {
                      ...message.metadata,
                      agent: {
                        ...message.metadata?.agent,
                        loaded_skills: skills,
                      },
                    },
                  }
                : message
            ),
          },
        });
      },

      finalizeStreamMessage: (messageId: string) => {
        const { currentConversation, streamingMessageId } = get();
        if (!currentConversation || !streamingMessageId) return;

        set({
          streamingMessageId: null,
          pendingQuestion: null,
          agentActivity: null,
          currentConversation: {
            ...currentConversation,
            messages: currentConversation.messages.map((msg) =>
              msg.id === streamingMessageId
                ? { ...msg, id: messageId }
                : msg
            ),
          },
        });
      },

      answerQuestion: async (conversationId, answer) => {
        const generation = captureSessionGeneration();
        // Clear the question being answered before resuming. An adapter can
        // synchronously advance to a second AskUserQuestion while the resume
        // HTTP request is still in flight; never clear pendingQuestion after
        // await, otherwise that newly arrived question is lost.
        const currentState = get();
        const answeredQuestion = currentState.pendingQuestion;
        const pendingRequests = Object.fromEntries(
          Object.entries(currentState.pendingRequests).map(([id, request]) => [
            id,
            request.status === 'pending'
              ? { ...request, status: 'resolved' as const }
              : request,
          ]),
        );
        set({
          error: null,
          pendingQuestion: null,
          pendingRequests,
          agentActivity: '正在提交回答…',
        });
        try {
          await resumeAgent(conversationId, answer);
          if (!sessionStillCurrent(generation)) return;
          if (!get().pendingQuestion) {
            set({ agentActivity: 'Agent 已继续运行…' });
          }
        } catch (error: any) {
          if (!sessionStillCurrent(generation)) throw error;
          // Restore the answered card only when no newer question arrived.
          if (!get().pendingQuestion) {
            set({
              pendingQuestion: answeredQuestion,
              pendingRequests: currentState.pendingRequests,
            });
          }
          set({
            error: error.message || '提交回答失败',
            agentActivity: answeredQuestion ? '等待你的回答' : null,
          });
          throw error;
        }
      },

      cancelTurn: async (conversationId) => {
        const generation = captureSessionGeneration();
        set({ error: null, agentActivity: '正在取消…' });
        try {
          await cancelAgentTurn(conversationId);
          if (!sessionStillCurrent(generation)) return;
        } catch (error: any) {
          if (!sessionStillCurrent(generation)) throw error;
          set({ error: error.message || '取消失败', agentActivity: null });
          throw error;
        }
      },
    }));

// The legacy transcript store is session-scoped in memory only (四次复审
// P0-R2): transcripts are business facts whose source of truth is the
// backend, and persisting them duplicated messages into localStorage across
// accounts, grew without bound and stayed after logout. A reload now
// restores the open conversation through workspace.conversationId →
// GET /conversations/:id/ instead of hydrating a stale transcript.
registerSessionReset(() => useConversationStore.getState().clearAll());
