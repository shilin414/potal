import React from 'react';
import { Avatar, Typography } from 'antd';
import { ThunderboltOutlined } from '@ant-design/icons';
import ReactMarkdown from 'react-markdown';
import type { AgentToolCall } from '@/services/agentProtocol';
import { useAuthStore } from '@/stores/useAuthStore';
import {
  agentAvatarFallback,
  agentAvatarUrl,
  agentDisplayName,
  formatUserLabel,
  userAvatarFallback,
  userAvatarUrl,
} from '@/lib/chatIdentity';
import './MessageList.css';

const { Text } = Typography;

interface Message {
  id: string;
  role: 'user' | 'assistant' | 'system';
  content: string;
  created_at: string;
  metadata?: {
    graphflow?: {
      tool_calls?: AgentToolCall[];
      loaded_skills?: string[];
    };
    agent?: {
      tool_calls?: AgentToolCall[];
      loaded_skills?: string[];
    };
  };
}

interface MessageListProps {
  messages: Message[];
  isLoading?: boolean;
  isStreaming?: boolean;
  /** The agent answering here (name/icon), so messages are signed by it
   *  instead of a generic 助手. */
  agent?: { name?: string; icon?: string; avatar_url?: string } | null;
}

const MessageList: React.FC<MessageListProps> = ({
  messages,
  isLoading = false,
  isStreaming = false,
  agent,
}) => {
  const { user } = useAuthStore();
  const formatToolValue = (value: string) => {
    if (!value) return '';
    try {
      return JSON.stringify(JSON.parse(value), null, 2);
    } catch {
      return value;
    }
  };

  const renderToolCall = (toolCall: AgentToolCall, index: number) => {
    const statusText: Record<string, string> = {
      running: '调用中',
      inProgress: '调用中',
      completed: '已完成',
      succeeded: '已完成',
      failed: '失败',
      cancelled: '已取消',
    };
    const hasDetails = Boolean(
      toolCall.input || toolCall.result || toolCall.error_message
    );
    return (
      <details
        key={toolCall.id || `${toolCall.name}-${index}`}
        className={`tool-call-row tool-call-row--${toolCall.status || 'running'}`}
      >
        <summary className="tool-call-summary">
          <span className="tool-call-dot" aria-hidden="true" />
          <span className="tool-call-name">{toolCall.name || '未知工具'}</span>
          <span className="tool-call-status">
            {statusText[toolCall.status] || toolCall.status || '调用中'}
          </span>
          {hasDetails && <span className="tool-call-expand" aria-hidden="true" />}
        </summary>
        {hasDetails && (
          <div className="tool-call-details">
            {toolCall.input && (
              <div>
                <span>输入</span>
                <pre>{formatToolValue(toolCall.input)}</pre>
              </div>
            )}
            {toolCall.result && (
              <div>
                <span>结果</span>
                <pre>{formatToolValue(toolCall.result)}</pre>
              </div>
            )}
            {toolCall.error_message && (
              <div className="tool-call-error">{toolCall.error_message}</div>
            )}
          </div>
        )}
      </details>
    );
  };

  const renderMessage = (message: Message) => {
    const isUser = message.role === 'user';
    const isSystem = message.role === 'system';
    const toolCalls = message.metadata?.agent?.tool_calls
      || message.metadata?.graphflow?.tool_calls
      || [];
    const loadedSkills = message.metadata?.agent?.loaded_skills
      || message.metadata?.graphflow?.loaded_skills
      || [];

    return (
      <div
        key={message.id}
        className={`animate-fade-in mb-4 flex gap-3 ${
          isUser ? 'flex-row-reverse' : 'flex-row'
        }`}
      >
        <Avatar
          size={36}
          className="run-chat-avatar flex-shrink-0"
          src={(isUser ? userAvatarUrl(user) : agentAvatarUrl(agent)) || undefined}
          style={{
            backgroundColor: isUser
              ? 'var(--color-primary)'
              : 'var(--color-bg-elevated)',
            color: isUser ? 'var(--color-on-primary)' : 'var(--color-primary)',
          }}
        >
          {isUser ? userAvatarFallback(user) : agentAvatarFallback(agent)}
        </Avatar>

        <div
          className={`max-w-[75%] rounded-2xl px-4 py-3 ${
            isUser
              ? 'border border-primary/30 text-text'
              : 'border border-border bg-card text-text'
          }`}
          style={isUser ? {
            background: 'color-mix(in srgb, var(--color-primary) 12%, var(--color-bg-card))',
          } : undefined}
        >
          <div
            className={`mb-1 flex items-center gap-2 text-xs ${
              isUser ? 'justify-end text-text-sec' : 'text-text-dim'
            }`}
          >
            <span className="font-medium chat-sender-name">
              {isUser ? formatUserLabel(user)
                : isSystem ? '系统' : agentDisplayName(agent)}
            </span>
            <span>
              {new Date(message.created_at).toLocaleString('zh-CN', {
                hour: '2-digit',
                minute: '2-digit',
              })}
            </span>
          </div>

          <div className={`text-sm leading-relaxed ${isUser ? '' : 'prose prose-sm dark:prose-invert max-w-none'}`}>
            {!isUser && toolCalls.length > 0 && (
              <div className="tool-call-list">
                {toolCalls.map(renderToolCall)}
              </div>
            )}
            {!isUser && loadedSkills.length > 0 && (
              <div className="message-loaded-skills">
                <ThunderboltOutlined /> 已加载技能：{loadedSkills.join('、')}
              </div>
            )}
            {isUser || isSystem ? (
              <span>{message.content}</span>
            ) : (
              <ReactMarkdown>{message.content}</ReactMarkdown>
            )}
          </div>
        </div>
      </div>
    );
  };

  return (
    <div className="flex flex-col px-4 py-6">
      {messages.length === 0 && !isLoading && (
        <div className="flex flex-1 items-center justify-center">
          <Text className="text-text-dim">开始新的对话吧...</Text>
        </div>
      )}
      {messages.map(renderMessage)}

      {(isLoading || isStreaming) && (
        <div className="animate-fade-in mb-4 flex gap-3">
          <Avatar
            size={36}
            className="run-chat-avatar flex-shrink-0"
            src={agentAvatarUrl(agent) || undefined}
            style={{
              backgroundColor: 'var(--color-bg-elevated)',
              color: 'var(--color-primary)',
            }}
          >
            {agentAvatarFallback(agent)}
          </Avatar>
          <div className="flex items-center gap-1 rounded-2xl border border-border bg-card px-4 py-3">
            <span className="animate-typing-dot inline-block h-2 w-2 rounded-full bg-primary" style={{ animationDelay: '0s' }} />
            <span className="animate-typing-dot inline-block h-2 w-2 rounded-full bg-primary" style={{ animationDelay: '0.2s' }} />
            <span className="animate-typing-dot inline-block h-2 w-2 rounded-full bg-primary" style={{ animationDelay: '0.4s' }} />
          </div>
        </div>
      )}
    </div>
  );
};

export default MessageList;
