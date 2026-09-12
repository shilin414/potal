/**
 * AgentEditorModal — 智能体市场「新建 / 编辑智能体」。
 *
 * Two authoring modes, because Studio has two kinds of agent:
 *
 *  - 「运行时智能体」(default, e.g. 飞书 Aily 自定义智能体): creates/edits an
 *    **Application + ApplicationRuntimeBinding** (§6/§12) — the entity the
 *    workspace can actually run. The form is rendered from
 *    `GET /api/v2/runtimes`, so no provider is hard-coded here.
 *  - 「本地创作智能体」: the legacy Agent row (system prompt + skills), kept for
 *    the existing GraphFlow workspace path.
 *
 * Editing an existing row locks the mode: an Application cannot become a local
 * Agent (and vice versa).
 */
import { useEffect, useMemo, useState } from 'react';
import { Alert, Button, Form, Input, Modal, Segmented, Select, Switch, message } from 'antd';
import { api } from '@/services/api';
import {
  createAgentApplication,
  fetchAgentRuntimes,
  updateAgentApplication,
  validateAgentRuntime,
  type AgentRuntimeDescriptor,
} from '@/services/runApi';

export type AgentEditorMode = 'runtime' | 'local';

interface AgentEditorModalProps {
  /** Application id (runtime agent) or Agent id (local agent), null = create. */
  agentId?: number | null;
  /** Which list `agentId` belongs to. */
  mode?: AgentEditorMode;
  open: boolean;
  onClose: () => void;
  onSaved: () => void | Promise<void>;
}

interface AgentCategoryOption {
  id: number;
  name: string;
  slug: string;
}

interface SkillOption {
  id: string;
  name: string;
  slug: string;
  description?: string;
  is_active: boolean;
}

interface LocalAgentDetail {
  name: string;
  slug: string;
  description: string;
  icon: string;
  category: { id: number };
  system_prompt: string;
  skill_bindings: Array<{ skill_id: string }>;
  is_public: boolean;
}

interface RuntimeAgentDetail {
  name: string;
  slug: string;
  description: string;
  icon: string;
  category_slug?: string;
  is_public: boolean;
  is_default_agent?: boolean;
  runtime_type?: string;
  provider_key?: string;
  external_resource_id?: string;
  identity_mode?: string;
  execution_mode?: string;
}

interface AgentFormValues {
  name: string;
  slug: string;
  description?: string;
  icon?: string;
  category?: number;
  category_slug?: string;
  system_prompt?: string;
  skill_ids?: string[];
  is_public: boolean;
  runtime_key?: string;
  external_resource_id?: string;
  identity_mode?: string;
  execution_mode?: string;
  set_default_agent?: boolean;
}

const unwrap = <T,>(response: T[] | { results?: T[] }): T[] => (
  Array.isArray(response) ? response : response.results ?? []
);

const fieldError = (error: any, fallback: string): string => {
  const data = error?.response?.data;
  if (!data || typeof data !== 'object') return error?.message || fallback;
  const first = Object.values(data).flat(3)[0];
  return typeof first === 'string' ? first : fallback;
};

const DEFAULT_CATEGORY = { slug: 'agents', name: '智能体' };

const AgentEditorModal = ({
  agentId = null,
  mode = 'runtime',
  open,
  onClose,
  onSaved,
}: AgentEditorModalProps) => {
  const [form] = Form.useForm<AgentFormValues>();
  const [categories, setCategories] = useState<AgentCategoryOption[]>([]);
  const [skills, setSkills] = useState<SkillOption[]>([]);
  const [runtimes, setRuntimes] = useState<AgentRuntimeDescriptor[]>([]);
  const [agentType, setAgentType] = useState<AgentEditorMode>(mode);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [validating, setValidating] = useState(false);
  const [validation, setValidation] = useState<
    { ok: boolean; checked: boolean; detail: string } | null>(null);

  const isEdit = Boolean(agentId);
  const runtimeKey = Form.useWatch('runtime_key', form);
  const selectedRuntime = useMemo(
    () => runtimes.find((item) => item.key === runtimeKey),
    [runtimes, runtimeKey],
  );

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setLoading(true);
    setValidation(null);
    setAgentType(mode);

    const detailRequest = agentId
      ? (mode === 'local'
        ? api.get<LocalAgentDetail>(`/agents/${agentId}/`)
        : api.get<RuntimeAgentDetail>(`/v2/applications/${agentId}`))
      : Promise.resolve(null);

    Promise.all([
      api.get<AgentCategoryOption[] | { results?: AgentCategoryOption[] }>('/agents/categories/'),
      api.get<SkillOption[] | { results?: SkillOption[] }>('/apps/skills/'),
      fetchAgentRuntimes().catch(() => [] as AgentRuntimeDescriptor[]),
      detailRequest,
    ])
      .then(([categoryResponse, skillResponse, runtimeList, detail]) => {
        if (cancelled) return;
        setCategories(unwrap(categoryResponse));
        setSkills(unwrap(skillResponse).filter((skill) => skill.is_active));
        setRuntimes(runtimeList);
        form.resetFields();

        if (!detail) {
          form.setFieldsValue({
            icon: '🤖',
            skill_ids: [],
            is_public: false,
            runtime_key: runtimeList[0]?.key,
            identity_mode: runtimeList[0]?.identity_modes[0] || 'user',
            execution_mode: runtimeList[0]?.execution_modes[0] || 'interactive',
            set_default_agent: false,
          });
          return;
        }
        if (mode === 'local') {
          const agent = detail as LocalAgentDetail;
          form.setFieldsValue({
            category: agent.category?.id,
            name: agent.name,
            slug: agent.slug,
            description: agent.description,
            icon: agent.icon,
            system_prompt: agent.system_prompt,
            skill_ids: agent.skill_bindings.map((binding) => binding.skill_id),
            is_public: agent.is_public,
          });
          return;
        }
        const application = detail as RuntimeAgentDetail;
        const descriptor = runtimeList.find((item) => (
          item.provider_key === application.provider_key
          && item.runtime_type === application.runtime_type));
        form.setFieldsValue({
          name: application.name,
          slug: application.slug,
          description: application.description,
          icon: application.icon,
          category_slug: application.category_slug,
          is_public: application.is_public,
          runtime_key: descriptor?.key || runtimeList[0]?.key,
          external_resource_id: application.external_resource_id,
          identity_mode: application.identity_mode || 'user',
          execution_mode: application.execution_mode || 'interactive',
          set_default_agent: Boolean(application.is_default_agent),
        });
      })
      .catch(() => message.error('加载智能体配置失败'))
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => { cancelled = true; };
  }, [agentId, form, mode, open]);

  const handleValidate = async () => {
    const descriptor = selectedRuntime;
    const resourceId = form.getFieldValue('external_resource_id');
    if (!descriptor) return;
    setValidating(true);
    setValidation(null);
    try {
      const result = await validateAgentRuntime({
        provider_key: descriptor.provider_key,
        runtime_type: descriptor.runtime_type,
        external_resource_id: resourceId,
        identity_mode: form.getFieldValue('identity_mode') || 'user',
      });
      setValidation(result);
      if (result.ok) message.success(result.detail);
    } catch (error: any) {
      setValidation({
        ok: false,
        checked: true,
        detail: fieldError(error, '校验失败'),
      });
    } finally {
      setValidating(false);
    }
  };

  const saveRuntimeAgent = async (values: AgentFormValues) => {
    const descriptor = runtimes.find((item) => item.key === values.runtime_key);
    if (!descriptor) {
      message.error('请选择智能体运行时');
      return;
    }
    const category = categories.find((item) => item.slug === values.category_slug);
    const runtime = {
      provider_key: descriptor.provider_key,
      runtime_type: descriptor.runtime_type,
      external_resource_id: values.external_resource_id || '',
      identity_mode: values.identity_mode || descriptor.identity_modes[0] || 'user',
      execution_mode: values.execution_mode
        || descriptor.execution_modes[0] || 'interactive',
    };
    if (isEdit && agentId) {
      await updateAgentApplication(agentId, {
        name: values.name,
        description: values.description || '',
        icon: values.icon || '',
        is_public: values.is_public,
        category_slug: values.category_slug || '',
        category_name: category?.name || '',
        runtime,
        set_default_agent: Boolean(values.set_default_agent),
      });
      message.success('智能体已更新');
      return;
    }
    await createAgentApplication({
      name: values.name,
      slug: values.slug,
      description: values.description || '',
      icon: values.icon || '',
      is_public: values.is_public,
      category_slug: values.category_slug || DEFAULT_CATEGORY.slug,
      category_name: category?.name || DEFAULT_CATEGORY.name,
      runtime,
      set_default_agent: Boolean(values.set_default_agent),
    });
    message.success('智能体已创建');
  };

  const saveLocalAgent = async (values: AgentFormValues) => {
    if (isEdit && agentId) {
      await api.patch(`/agents/${agentId}/`, {
        category: values.category,
        name: values.name,
        description: values.description,
        icon: values.icon,
        system_prompt: values.system_prompt,
        skill_ids: values.skill_ids || [],
        is_public: values.is_public,
      });
      message.success('智能体已更新');
      return;
    }
    await api.post('/agents/', {
      category: values.category,
      name: values.name,
      slug: values.slug,
      description: values.description,
      icon: values.icon,
      system_prompt: values.system_prompt,
      skill_ids: values.skill_ids || [],
      is_public: values.is_public,
    });
    message.success('智能体已创建');
  };

  const handleSave = async () => {
    try {
      const values = await form.validateFields();
      setSaving(true);
      if (agentType === 'runtime') {
        await saveRuntimeAgent(values);
      } else {
        await saveLocalAgent(values);
      }
      await onSaved();
      onClose();
    } catch (error: any) {
      if (!error?.errorFields) {
        message.error(fieldError(error, '保存智能体失败'));
      }
    } finally {
      setSaving(false);
    }
  };

  const typeOptions = useMemo(() => {
    const runtimeLabel = runtimes[0]?.label || '平台智能体';
    return [
      { label: runtimeLabel, value: 'runtime' as AgentEditorMode },
      { label: '本地创作智能体', value: 'local' as AgentEditorMode },
    ];
  }, [runtimes]);

  const resourceRules = selectedRuntime?.resource_id_required
    ? [{
      required: true,
      message: `请输入${selectedRuntime.resource_id_label || '资源 ID'}`,
    }, ...(selectedRuntime.resource_id_pattern ? [{
      pattern: new RegExp(selectedRuntime.resource_id_pattern),
      message: selectedRuntime.resource_id_hint || '格式不正确',
    }] : [])]
    : [];

  return (
    <Modal
      title={isEdit ? '编辑智能体' : '新建智能体'}
      open={open}
      onCancel={onClose}
      onOk={handleSave}
      confirmLoading={saving}
      okText={isEdit ? '保存' : '创建'}
      cancelText="取消"
      width={760}
      loading={loading}
      destroyOnClose
    >
      <Form form={form} layout="vertical" requiredMark="optional">
        {!isEdit && typeOptions.length > 1 && (
          <Form.Item label="智能体类型">
            <Segmented
              value={agentType}
              options={typeOptions}
              onChange={(value) => setAgentType(value as AgentEditorMode)}
            />
          </Form.Item>
        )}

        {agentType === 'runtime' ? (
          <>
            <Form.Item
              name="runtime_key"
              label="运行时"
              rules={[{ required: true, message: '请选择运行时' }]}
              extra={selectedRuntime?.label
                ? `在「${selectedRuntime.label}」平台上运行的智能体，Studio 只保留引用。`
                : '暂无可用的运行时提供方，请联系平台管理员配置 Provider。'}
            >
              <Select
                placeholder="选择运行时"
                options={runtimes.map((item) => ({
                  value: item.key,
                  label: item.label,
                }))}
              />
            </Form.Item>
            <Form.Item
              name="external_resource_id"
              label={selectedRuntime?.resource_id_label || 'Agent ID'}
              extra={selectedRuntime?.resource_id_hint}
              rules={resourceRules}
            >
              <Input
                placeholder="agent_xxxxxxxx"
                allowClear
                onBlur={() => setValidation(null)}
              />
            </Form.Item>
            {validation && (
              <Alert
                type={validation.ok ? 'success' : (validation.checked ? 'warning' : 'info')}
                showIcon
                message={validation.detail}
                style={{ marginBottom: 16 }}
              />
            )}
            <div className="agent-form-row">
              <Form.Item
                name="identity_mode"
                label="身份模式"
                extra="user 用调用者自己的飞书身份（推荐）。"
              >
                <Select
                  options={(selectedRuntime?.identity_modes || ['user']).map((mode) => ({
                    value: mode,
                    label: mode === 'user' ? 'user（用户身份 UAT）' : 'tenant（应用身份 TAT）',
                  }))}
                />
              </Form.Item>
              <Form.Item
                name="execution_mode"
                label="执行模式"
                extra="interactive 走流式输出。"
              >
                <Select
                  options={(selectedRuntime?.execution_modes || ['interactive']).map((mode) => ({
                    value: mode,
                    label: mode === 'interactive' ? 'interactive（流式）' : 'background（轮询）',
                  }))}
                />
              </Form.Item>
            </div>
          </>
        ) : null}

        <div className="agent-form-row">
          <Form.Item
            name="name"
            label="名称"
            rules={[{ required: true, whitespace: true, message: '请输入智能体名称' }]}
          >
            <Input placeholder="例如：销售助手" maxLength={100} />
          </Form.Item>
          <Form.Item
            name="slug"
            label="标识"
            extra={isEdit ? '创建后不可修改。' : '工作台地址用的短标识。'}
            rules={agentType === 'local' || !isEdit ? [
              { required: true, message: '请输入唯一标识' },
              { pattern: /^[a-z0-9]+(?:-[a-z0-9]+)*$/, message: '仅支持小写字母、数字和连字符' },
            ] : []}
          >
            <Input placeholder="sales-assistant" maxLength={100} disabled={isEdit} />
          </Form.Item>
        </div>

        <div className="agent-form-row">
          {agentType === 'local' ? (
            <Form.Item
              name="category"
              label="分类"
              preserve={false}
              rules={[{ required: true, message: '请选择分类' }]}
            >
              <Select
                placeholder="选择智能体分类"
                options={categories.map((category) => ({
                  value: category.id,
                  label: category.name,
                }))}
              />
            </Form.Item>
          ) : (
            <Form.Item
              name="category_slug"
              label="分类"
              preserve={false}
              extra="用于智能体市场的分类筛选。"
            >
              <Select
                allowClear
                placeholder="智能体（默认）"
                options={[
                  { value: DEFAULT_CATEGORY.slug, label: DEFAULT_CATEGORY.name },
                  ...categories.map((category) => ({
                    value: category.slug,
                    label: category.name,
                  })),
                ]}
              />
            </Form.Item>
          )}
          <Form.Item name="icon" label="图标（emoji）" extra="未上传头像时展示。">
            <Input placeholder="🤖" maxLength={50} />
          </Form.Item>
        </div>

        <Form.Item
          name="description"
          label="描述"
          rules={[{ required: true, whitespace: true, message: '请输入智能体描述' }]}
        >
          <Input.TextArea rows={3} placeholder="说明这个智能体适合完成什么任务" />
        </Form.Item>

        {agentType === 'local' ? (
          <>
            <Form.Item
              name="system_prompt"
              label="系统提示词"
              extra="定义智能体的身份、能力边界、工作流程和输出要求。"
              rules={[{ required: true, whitespace: true, message: '请输入系统提示词' }]}
            >
              <Input.TextArea
                rows={8}
                placeholder="你是一位专业的……"
                className="agent-system-prompt-input"
              />
            </Form.Item>
            <Form.Item
              name="skill_ids"
              label="使用的 Skill"
              extra="选中的 Skill 会在智能体会话启动时按顺序加载。"
            >
              <Select
                mode="multiple"
                allowClear
                showSearch
                optionFilterProp="label"
                placeholder="选择要加载的 Skill"
                options={skills.map((skill) => ({
                  value: skill.id,
                  label: `${skill.name} (${skill.slug})`,
                  title: skill.description,
                }))}
              />
            </Form.Item>
          </>
        ) : null}

        <div className="agent-form-row agent-form-row-compact">
          <Form.Item name="is_public" label="公开到智能体市场" valuePropName="checked">
            <Switch checkedChildren="公开" unCheckedChildren="仅自己可见" />
          </Form.Item>
          {agentType === 'runtime' ? (
            <Form.Item
              name="set_default_agent"
              label="设为工作台默认智能体"
              valuePropName="checked"
              extra="首页输入框与未知 @ 都会交给它。"
            >
              <Switch checkedChildren="默认" unCheckedChildren="否" />
            </Form.Item>
          ) : null}
        </div>

        {agentType === 'runtime' && (
          <div className="agent-editor-hint">
            <Button
              size="small"
              onClick={handleValidate}
              loading={validating}
              disabled={!selectedRuntime}
            >
              校验可见性
            </Button>
            <span>
              校验会以你本人的身份调用运行时接口；头像可在保存后于卡片上「改头像」。
            </span>
          </div>
        )}
      </Form>
    </Modal>
  );
};

export default AgentEditorModal;
