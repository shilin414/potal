import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Button,
  Empty,
  Form,
  Input,
  List,
  Modal,
  Segmented,
  Select,
  Spin,
  Tag,
  Tooltip,
  message,
} from 'antd';
import {
  DeleteOutlined,
  EditOutlined,
  FileTextOutlined,
  PlusOutlined,
  ReloadOutlined,
  SaveOutlined,
  SearchOutlined,
} from '@ant-design/icons';
import {
  runtimeSkillApi,
  type RuntimeSkillDetail,
  type RuntimeSkillRoot,
  type RuntimeSkillSummary,
  type SkillProvider,
} from '@/services/runtimeSkills';
import './SkillsPage.css';

type ProviderFilter = 'all' | SkillProvider;

const providerColor: Record<SkillProvider, string> = {
  codex: 'gold',
  graphflow: 'purple',
};

const errorText = (error: any, fallback: string) =>
  error?.response?.data?.detail || error?.message || fallback;

const SkillsPage = () => {
  const [roots, setRoots] = useState<RuntimeSkillRoot[]>([]);
  const [skills, setSkills] = useState<RuntimeSkillSummary[]>([]);
  const [canManage, setCanManage] = useState(false);
  const [loading, setLoading] = useState(true);
  const [detailLoading, setDetailLoading] = useState(false);
  const [selectedKey, setSelectedKey] = useState('');
  const [detail, setDetail] = useState<RuntimeSkillDetail | null>(null);
  const [provider, setProvider] = useState<ProviderFilter>('all');
  const [query, setQuery] = useState('');
  const [editing, setEditing] = useState(false);
  const [content, setContent] = useState('');
  const [saving, setSaving] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [createForm] = Form.useForm();

  const loadSkills = useCallback(async (preferredKey?: string) => {
    setLoading(true);
    try {
      const response = await runtimeSkillApi.list();
      setRoots(response.roots);
      setSkills(response.skills);
      setCanManage(response.canManage);
      const wanted = preferredKey || selectedKey;
      const next = response.skills.some(
        (skill) => `${skill.provider}:${skill.slug}` === wanted,
      ) ? wanted : response.skills[0]
        ? `${response.skills[0].provider}:${response.skills[0].slug}`
        : '';
      setSelectedKey(next);
      if (!next) setDetail(null);
    } catch (error) {
      message.error(errorText(error, '加载技能失败'));
    } finally {
      setLoading(false);
    }
  }, [selectedKey]);

  useEffect(() => {
    void loadSkills();
    // Initial discovery only; later refreshes are explicit.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!selectedKey) return;
    const separator = selectedKey.indexOf(':');
    const selectedProvider = selectedKey.slice(0, separator) as SkillProvider;
    const slug = selectedKey.slice(separator + 1);
    setDetailLoading(true);
    setEditing(false);
    runtimeSkillApi.get(selectedProvider, slug)
      .then((value) => {
        setDetail(value);
        setContent(value.content);
      })
      .catch((error) => {
        setDetail(null);
        message.error(errorText(error, '加载技能详情失败'));
      })
      .finally(() => setDetailLoading(false));
  }, [selectedKey]);

  const filteredSkills = useMemo(() => {
    const keyword = query.trim().toLowerCase();
    return skills.filter((skill) => {
      if (provider !== 'all' && skill.provider !== provider) return false;
      if (!keyword) return true;
      return [skill.name, skill.slug, skill.description]
        .some((value) => value.toLowerCase().includes(keyword));
    });
  }, [provider, query, skills]);

  const save = async () => {
    if (!detail || content === detail.content) {
      setEditing(false);
      return;
    }
    setSaving(true);
    try {
      const updated = await runtimeSkillApi.update(
        detail.provider, detail.slug, content);
      setDetail(updated);
      setContent(updated.content);
      setEditing(false);
      message.success('技能已保存');
      await loadSkills(`${updated.provider}:${updated.slug}`);
    } catch (error) {
      message.error(errorText(error, '保存技能失败'));
    } finally {
      setSaving(false);
    }
  };

  const remove = () => {
    if (!detail) return;
    Modal.confirm({
      title: `删除技能“${detail.name}”？`,
      content: `将删除目录 ${detail.path} 及其中全部文件，此操作不可撤销。`,
      okText: '删除',
      okButtonProps: { danger: true },
      cancelText: '取消',
      onOk: async () => {
        try {
          await runtimeSkillApi.remove(detail.provider, detail.slug);
          message.success('技能已删除');
          setDetail(null);
          setSelectedKey('');
          await loadSkills();
        } catch (error) {
          message.error(errorText(error, '删除技能失败'));
          throw error;
        }
      },
    });
  };

  const create = async () => {
    const values = await createForm.validateFields();
    setCreating(true);
    try {
      const created = await runtimeSkillApi.create(values);
      setCreateOpen(false);
      createForm.resetFields();
      message.success('技能已创建');
      await loadSkills(`${created.provider}:${created.slug}`);
    } catch (error) {
      message.error(errorText(error, '创建技能失败'));
    } finally {
      setCreating(false);
    }
  };

  return (
    <div className="skills-page animate-fade-in">
      <div className="skills-page-header">
        <div>
          <h1>技能管理</h1>
          <p>查看和维护 Codex 与 GraphFlow 使用的文件系统技能。</p>
        </div>
        <div className="skills-header-actions">
          <Button icon={<ReloadOutlined />} onClick={() => void loadSkills()}>
            刷新
          </Button>
          {canManage && (
            <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>
              新增技能
            </Button>
          )}
        </div>
      </div>

      <div className="skill-root-grid">
        {roots.map((root) => (
          <button
            type="button"
            key={root.provider}
            className={`skill-root-card ${provider === root.provider ? 'active' : ''}`}
            onClick={() => setProvider(
              provider === root.provider ? 'all' : root.provider)}
          >
            <div className="skill-root-title">
              <Tag color={providerColor[root.provider]}>{root.label}</Tag>
              <strong>{root.skillCount} 个技能</strong>
            </div>
            <Tooltip title={root.path}>
              <code>{root.path}</code>
            </Tooltip>
            <span className={root.exists ? 'root-ready' : 'root-missing'}>
              {root.exists ? '目录可用' : '目录尚未创建'}
            </span>
          </button>
        ))}
      </div>

      <div className="skills-workbench">
        <section className="skills-list-panel">
          <div className="skills-list-toolbar">
            <Input
              allowClear
              prefix={<SearchOutlined />}
              placeholder="搜索技能"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
            <Segmented
              block
              value={provider}
              onChange={(value) => setProvider(value as ProviderFilter)}
              options={[
                { label: '全部', value: 'all' },
                { label: 'Codex', value: 'codex' },
                { label: 'GraphFlow', value: 'graphflow' },
              ]}
            />
          </div>
          <Spin spinning={loading}>
            <List
              className="runtime-skill-list"
              dataSource={filteredSkills}
              locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无技能" /> }}
              renderItem={(skill) => {
                const key = `${skill.provider}:${skill.slug}`;
                return (
                  <List.Item
                    className={selectedKey === key ? 'selected' : ''}
                    onClick={() => setSelectedKey(key)}
                  >
                    <div className="runtime-skill-list-item">
                      <div>
                        <strong>{skill.name}</strong>
                        <Tag color={providerColor[skill.provider]}>{skill.providerLabel}</Tag>
                      </div>
                      <code>{skill.slug}</code>
                      <p>{skill.description || '暂无描述'}</p>
                    </div>
                  </List.Item>
                );
              }}
            />
          </Spin>
        </section>

        <section className="skill-detail-panel">
          <Spin spinning={detailLoading}>
            {!detail ? (
              <div className="skill-detail-empty">
                <Empty description="选择一个技能查看详情" />
              </div>
            ) : (
              <>
                <div className="skill-detail-header">
                  <div>
                    <div className="skill-detail-title">
                      <h2>{detail.name}</h2>
                      <Tag color={providerColor[detail.provider]}>{detail.providerLabel}</Tag>
                    </div>
                    <p>{detail.description || '暂无描述'}</p>
                  </div>
                  {canManage && (
                    <div className="skill-detail-actions">
                      {editing ? (
                        <>
                          <Button onClick={() => {
                            setContent(detail.content);
                            setEditing(false);
                          }}>取消</Button>
                          <Button
                            type="primary"
                            icon={<SaveOutlined />}
                            loading={saving}
                            onClick={() => void save()}
                          >保存</Button>
                        </>
                      ) : (
                        <Button icon={<EditOutlined />} onClick={() => setEditing(true)}>
                          编辑
                        </Button>
                      )}
                      <Button danger icon={<DeleteOutlined />} onClick={remove}>
                        删除
                      </Button>
                    </div>
                  )}
                </div>

                <div className="skill-path-row">
                  <FileTextOutlined />
                  <code>{detail.entrypoint}</code>
                </div>

                <div className="skill-content-editor">
                  <div className="skill-section-label">SKILL.md</div>
                  {editing ? (
                    <Input.TextArea
                      value={content}
                      onChange={(event) => setContent(event.target.value)}
                      autoSize={false}
                      spellCheck={false}
                    />
                  ) : (
                    <pre>{detail.content}</pre>
                  )}
                </div>

                <div className="skill-files">
                  <div className="skill-section-label">技能文件（{detail.fileCount}）</div>
                  <div className="skill-file-list">
                    {detail.files.map((file) => (
                      <div key={file.path}>
                        <span>{file.path}</span>
                        <small>{file.size.toLocaleString()} B</small>
                      </div>
                    ))}
                  </div>
                </div>
              </>
            )}
          </Spin>
        </section>
      </div>

      <Modal
        title="新增运行时技能"
        open={createOpen}
        okText="创建"
        cancelText="取消"
        onOk={create}
        confirmLoading={creating}
        onCancel={() => {
          setCreateOpen(false);
          createForm.resetFields();
        }}
        destroyOnClose
      >
        <Form form={createForm} layout="vertical" initialValues={{ provider: 'codex' }}>
          <Form.Item name="provider" label="运行时" rules={[{ required: true }]}>
            <Select options={[
              { label: 'Codex', value: 'codex' },
              { label: 'GraphFlow', value: 'graphflow' },
            ]} />
          </Form.Item>
          <Form.Item
            name="slug"
            label="目录名称"
            rules={[
              { required: true, message: '请输入目录名称' },
              { pattern: /^[A-Za-z0-9][A-Za-z0-9._-]*$/, message: '目录名称格式不正确' },
            ]}
          >
            <Input placeholder="my-skill" />
          </Form.Item>
          <Form.Item name="name" label="技能名称">
            <Input placeholder="我的技能" />
          </Form.Item>
          <Form.Item name="description" label="描述">
            <Input.TextArea rows={3} placeholder="说明技能适用的任务和触发条件" />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
};

export default SkillsPage;
