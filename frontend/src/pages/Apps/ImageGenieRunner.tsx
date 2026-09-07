import React, { useEffect, useRef, useState } from 'react';
import { Button, Result, Spin } from 'antd';
import { ArrowLeftOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '@/stores/useAppStore';
import { api } from '@/services/api';
import { useAppWorkspace } from '@/hooks/useAppWorkspace';
import type { AppItem } from '@/types';
import MessageInput from '@/components/Chat/MessageInput';
import AssetPanel from '@/components/Workspace/AssetPanel';
import './ImageGenieRunner.css';

// This runner is mounted at the static route /apps/image-genie/run (no :id param),
// so the slug is fixed here rather than read from useParams.
const IMAGE_GENIE_SLUG = 'image-genie';

interface GenMessage {
  id: string;
  role: 'user' | 'assistant';
  content: string;        // user prompt, or assistant caption / error text
  imageUrl?: string;      // assistant image messages only
  error?: boolean;
}

const SUGGESTIONS = [
  '一只穿着宇航服的柴犬在月球上弹吉他，赛博朋克霓虹光影',
  '中国传统水墨风格，山水之间的孤舟与蓑笠翁',
  '极简扁平插画，一个咖啡杯冒着爱心形状的热气',
  '写实摄影，清晨阳光穿过森林薄雾的丁达尔效应',
];

interface ImageRunnerConfig {
  empty_state_title?: string;
  input_placeholder?: string;
  input_hint?: string;
  prompt_prefix?: string;
  suggestions?: string[];
  size?: string;
}

interface ImageGenieRunnerProps {
  application?: AppItem;
  embedded?: boolean;
  projectId?: number;
}

const ImageGenieRunner: React.FC<ImageGenieRunnerProps> = ({ application, embedded, projectId }) => {
  const navigate = useNavigate();
  const { loadApp } = useAppStore();

  const [app, setApp] = useState<AppItem | null>(application ?? null);
  const [loadingApp, setLoadingApp] = useState(!application);
  // Each app run owns a workspace; generated images are saved into it.
  const {
    assets,
    addImageAsset,
    isWorkspaceReady,
    workspaceError,
  } = useAppWorkspace(app, projectId);
  const [messages, setMessages] = useState<GenMessage[]>([]);
  const [inputValue, setInputValue] = useState('');
  const [isGenerating, setIsGenerating] = useState(false);

  const config = (application?.runtime?.default_config ?? {}) as ImageRunnerConfig;
  const suggestions = Array.isArray(config.suggestions) && config.suggestions.length > 0
    ? config.suggestions
    : SUGGESTIONS;
  const emptyStateTitle = config.empty_state_title || '描述你想要的画面，AI 为你生成';
  const inputPlaceholder = config.input_placeholder || '描述你想生成的图片…（Enter 生成，Shift+Enter 换行）';
  const inputHint = config.input_hint || 'Enter 生成 · Shift+Enter 换行';

  const counter = useRef(0);
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const nextId = () => `m${++counter.current}`;

  useEffect(() => {
    if (application) {
      setApp(application);
      setLoadingApp(false);
      return;
    }
    setLoadingApp(true);
    loadApp(IMAGE_GENIE_SLUG)
      .then(setApp)
      .finally(() => setLoadingApp(false));
  }, [application, loadApp]);

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages, isGenerating]);

  const handleSend = async (prompt: string) => {
    const text = prompt.trim();
    if (!text || isGenerating || !isWorkspaceReady) return;

    setMessages((prev) => [...prev, { id: nextId(), role: 'user', content: text }]);
    setInputValue('');
    setIsGenerating(true);

    try {
      const requestPrompt = config.prompt_prefix
        ? `${config.prompt_prefix}\n${text}`
        : text;
      const data = await api.post<any>('/apps/image-generate/', {
        prompt: requestPrompt,
        ...(config.size ? { size: config.size } : {}),
      });
      setMessages((prev) => [
        ...prev,
        {
          id: nextId(),
          role: 'assistant',
          content: data.revised_prompt || text,
          imageUrl: data.image_url,
        },
      ]);
      // Persist generated image to the app's workspace
      if (data.image_url) {
        addImageAsset(data.image_url, data.revised_prompt || text);
      }
    } catch (error: any) {
      const detail = error?.response?.data?.detail || '图片生成失败，请稍后重试。';
      setMessages((prev) => [
        ...prev,
        { id: nextId(), role: 'assistant', content: detail, error: true },
      ]);
    } finally {
      setIsGenerating(false);
    }
  };

  if (loadingApp) {
    return (
      <div className="image-runner-page">
        <div className="flex items-center justify-center py-20">
          <Spin size="large" />
        </div>
      </div>
    );
  }

  if (!app) {
    return (
      <div className="image-runner-page">
        <Result
          status="404"
          title="应用不存在"
          subTitle="该应用可能已下线或链接有误。"
          extra={
            <Button type="primary" onClick={() => navigate('/apps')}>
              返回应用中心
            </Button>
          }
        />
      </div>
    );
  }

  if (workspaceError) {
    return (
      <div className="image-runner-page">
        <Result status="error" title={workspaceError} />
      </div>
    );
  }

  const isEmpty = messages.length === 0;

  return (
    <div className="image-runner-page animate-fade-in">
      {!embedded && <div className="image-runner-topbar">
        <Button
          type="text"
          icon={<ArrowLeftOutlined />}
          onClick={() => navigate('/apps')}
          className="image-runner-back"
        >
          返回应用中心
        </Button>
        <div className="image-runner-app">
          <span className="image-runner-emoji">{app.icon}</span>
          <span className="image-runner-name">{app.name}</span>
        </div>
      </div>}

      <div className="image-runner-main">
        <div className="image-runner-stage">
        {isEmpty && !isGenerating ? (
          <div className="image-runner-empty">
            <div
              className="image-runner-empty-icon"
              style={
                app.color
                  ? { background: `linear-gradient(135deg, ${app.color}, color-mix(in srgb, ${app.color} 40%, #000))` }
                  : undefined
              }
            >
              {app.icon}
            </div>
            <h2 className="image-runner-empty-title">{emptyStateTitle}</h2>
            <p className="image-runner-empty-sub">{app.description}</p>
            <div className="image-runner-suggestions">
              {suggestions.map((s) => (
                <button key={s} className="image-runner-suggestion" onClick={() => setInputValue(s)}>
                  {s}
                </button>
              ))}
            </div>
          </div>
        ) : (
          <div className="image-runner-messages">
            {messages.map((m) =>
              m.role === 'user' ? (
                <div key={m.id} className="image-runner-bubble image-runner-bubble--user animate-fade-in">
                  <div className="image-runner-bubble-text">{m.content}</div>
                </div>
              ) : (
                <div key={m.id} className="image-runner-bubble image-runner-bubble--assistant animate-fade-in">
                  {m.imageUrl ? (
                    <>
                      <img
                        className="image-runner-image"
                        src={m.imageUrl}
                        alt={m.content}
                        onClick={() => window.open(m.imageUrl, '_blank', 'noopener,noreferrer')}
                      />
                      {m.content && <div className="image-runner-bubble-text">{m.content}</div>}
                    </>
                  ) : (
                    <div className={`image-runner-bubble-text${m.error ? ' image-runner-bubble-text--error' : ''}`}>
                      {m.content}
                    </div>
                  )}
                </div>
              )
            )}
            {isGenerating && (
              <div className="image-runner-bubble image-runner-bubble--assistant image-runner-bubble--loading animate-fade-in">
                <Spin size="small" />
                <span className="image-runner-loading-text">正在生成图片…</span>
              </div>
            )}
            <div ref={messagesEndRef} />
          </div>
        )}

        <div className="image-runner-input-area">
          <div className="image-runner-input-wrapper">
            <MessageInput
              value={inputValue}
              onValueChange={setInputValue}
              onSendMessage={handleSend}
              disabled={isGenerating || !isWorkspaceReady}
              workspaceLocked
              placeholder={inputPlaceholder}
            />
            <div className="image-runner-input-hint">{inputHint}</div>
          </div>
        </div>
      </div>

      <AssetPanel
        assets={assets}
        hint="生成的图片会自动保存到这里"
      />
      </div>
    </div>
  );
};

export default ImageGenieRunner;
