import { useEffect, useMemo, useState } from 'react';
import { Input, InputNumber, Modal, Select, Spin, message } from 'antd';
import { useSearchParams } from 'react-router-dom';
import { RunChatPanel } from '@/components/Chat';
import GuidedPromptPanel from '@/components/Workspace/GuidedPromptPanel';
import { api } from '@/services/api';
import { useRunChatStore } from '@/stores/useRunChatStore';
import type { ApplicationRuntime, GuidedPrompt, GuidedQuestion } from '@/types';
import type { WorkflowProcess } from '@/types/workflow';
import './ChatApplicationView.css';

interface Props {
  application: ApplicationRuntime;
  projectId?: number;
  workflowStepRunId?: string;
}

const ChatApplicationView: React.FC<Props> = ({ application }) => {
  const [searchParams, setSearchParams] = useSearchParams();
  const requestedConversationId = searchParams.get('conversation');
  const [conversationId, setConversationId] = useState<number | null>(null);
  const [activePrompt, setActivePrompt] = useState<GuidedPrompt | null>(null);
  const [answers, setAnswers] = useState<Record<string, unknown>>({});
  const [composing, setComposing] = useState(false);
  const [draftRequest, setDraftRequest] = useState<{ id: number; text: string } | null>(null);
  const [conversationLookupComplete, setConversationLookupComplete] = useState(false);
  const loadConversation = useRunChatStore((state) => state.loadConversation);

  const guidedEntryProcess = useMemo<WorkflowProcess | null>(() => {
    const configuredKey = application.default_config.guided_entry_prompt_key;
    if (typeof configuredKey !== 'string') return null;
    const prompt = application.guided_prompts.find((item) => item.key === configuredKey);
    if (!prompt) return null;
    return {
      id: prompt.key,
      name: prompt.title,
      icon: prompt.icon,
      description: prompt.description,
      mode: 'guided',
      prompt_template: prompt.prompt_template,
      fields: prompt.questions.map((question) => ({
        id: question.key,
        label: question.label,
        type: question.type === 'single_choice'
          ? 'select'
          : question.type === 'multi_choice'
            ? 'multiselect'
            : question.type,
        options: question.options.map((option) => option.label),
        placeholder: question.placeholder,
        required: question.required,
      })),
    };
  }, [application.default_config, application.guided_prompts]);

  // Restore the most recent conversation for this application (v2 chain
  // keeps Conversations in the same backend table, so history survives).
  useEffect(() => {
    let active = true;
    setConversationLookupComplete(false);
    const restoreConversation = async () => {
      try {
        let restored: string | null = requestedConversationId;
        if (!restored) {
          const response = await api.get<any[] | { results?: any[] }>(
            '/conversations/', { application_id: application.id });
          const items = Array.isArray(response) ? response : response.results ?? [];
          restored = items[0]?.id ? String(items[0].id) : null;
        }
        const numeric = restored ? Number(restored) : null;
        if (numeric && Number.isInteger(numeric)) {
          await loadConversation(numeric);
          if (active) setConversationId(numeric);
        } else if (active) {
          setConversationId(null);
        }
      } catch {
        if (active) setConversationId(null);
      } finally {
        if (active) setConversationLookupComplete(true);
      }
    };
    void restoreConversation();
    return () => { active = false; };
  }, [application.id, requestedConversationId, loadConversation]);

  const suggestions = useMemo(() => application.guided_prompts
    .filter((prompt) => prompt.questions.length === 0)
    .map((prompt) => ({
      icon: prompt.icon,
      label: prompt.title,
      text: prompt.prompt_template,
    })), [application.guided_prompts]);

  const updateAnswer = (question: GuidedQuestion, value: unknown) => {
    setAnswers((current) => ({ ...current, [question.key]: value }));
  };

  const compose = async () => {
    if (!activePrompt) return;
    setComposing(true);
    try {
      const result = await api.post<{ prompt: string }>(
        `/apps/${application.application_slug}/compose-prompt/`,
        { prompt_id: activePrompt.id, answers },
      );
      setDraftRequest({ id: Date.now(), text: result.prompt });
      setActivePrompt(null);
      setAnswers({});
    } catch (error: any) {
      message.error(error?.response?.data?.detail || '问题选项校验失败');
    } finally {
      setComposing(false);
    }
  };

  if (!conversationLookupComplete) {
    return <div style={{ display: 'grid', height: '100%', placeItems: 'center' }}><Spin /></div>;
  }

  const defaultText = draftRequest?.text;
  if (defaultText) {
    return (
      <RunChatPanel
        applicationId={application.id}
        conversationId={conversationId}
        onConversationCreated={(id) => {
          setConversationId(id);
          setSearchParams({ conversation: String(id) }, { replace: true });
        }}
        title={application.chat_profile?.empty_state_title || application.application_name}
        description={application.chat_profile?.welcome_message || application.application_description}
        suggestions={suggestions}
        draftText={defaultText}
      />
    );
  }

  if (guidedEntryProcess && !conversationId) {
    return (
      <GuidedPromptPanel
        process={guidedEntryProcess}
        applying={false}
        onStart={(prompt) => setDraftRequest({ id: Date.now(), text: prompt })}
      />
    );
  }

  return (
    <>
      <div className="chat-application-layout">
        <div className="chat-application-main">
          <RunChatPanel
            applicationId={application.id}
            conversationId={conversationId}
            onConversationCreated={(id) => {
              setConversationId(id);
              setSearchParams({ conversation: String(id) }, { replace: true });
            }}
            title={application.chat_profile?.empty_state_title || application.application_name}
            description={application.chat_profile?.welcome_message || application.application_description}
            suggestions={suggestions}
          />
        </div>
      </div>

      <Modal
        title={activePrompt?.title}
        open={Boolean(activePrompt)}
        okText="生成提示词"
        cancelText="取消"
        confirmLoading={composing}
        onOk={compose}
        onCancel={() => { setActivePrompt(null); setAnswers({}); }}
      >
        {activePrompt?.description && <p>{activePrompt.description}</p>}
        {activePrompt?.questions.map((question) => (
          <div key={question.id} style={{ marginBottom: 18 }}>
            <label style={{ display: 'block', marginBottom: 8, fontWeight: 600 }}>
              {question.label}{question.required && <span style={{ color: '#ff4d4f' }}> *</span>}
            </label>
            {question.type === 'single_choice' && (
              <Select style={{ width: '100%' }}
                value={answers[question.key] as string | undefined}
                placeholder={question.placeholder || `请选择${question.label}`}
                options={question.options.map((option) => ({ value: option.value, label: option.label }))}
                onChange={(value) => updateAnswer(question, value)} />
            )}
            {question.type === 'multi_choice' && (
              <Select mode="multiple" style={{ width: '100%' }}
                value={answers[question.key] as string[] | undefined}
                placeholder={question.placeholder || `请选择${question.label}`}
                options={question.options.map((option) => ({ value: option.value, label: option.label }))}
                onChange={(value) => updateAnswer(question, value)} />
            )}
            {question.type === 'number' && (
              <InputNumber style={{ width: '100%' }}
                value={answers[question.key] as number | undefined}
                placeholder={question.placeholder}
                onChange={(value) => updateAnswer(question, value)} />
            )}
            {(question.type === 'text' || question.type === 'file') && (
              <Input.TextArea rows={question.type === 'text' ? 3 : 1}
                value={(answers[question.key] as string | undefined) || ''}
                placeholder={question.type === 'file'
                  ? '请输入工作区中的素材名称'
                  : question.placeholder}
                onChange={(event) => updateAnswer(question, event.target.value)} />
            )}
            {question.help_text && (
              <div style={{ marginTop: 5, color: 'var(--text-secondary)' }}>
                {question.help_text}
              </div>
            )}
          </div>
        ))}
      </Modal>
    </>
  );
};

export default ChatApplicationView;
