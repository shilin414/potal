import React, { useMemo, useState } from 'react';
import { Input, InputNumber, Select, Button, message } from 'antd';
import { ThunderboltOutlined, RocketOutlined } from '@ant-design/icons';
import { buildPromptFromTemplate } from '@/lib/templateWorkflow';
import type { WorkflowProcess } from '@/types/workflow';
import './GuidedPromptPanel.css';

interface GuidedPromptPanelProps {
  process: WorkflowProcess;
  applying: boolean;
  onStart: (prompt: string) => void;
}

const GuidedPromptPanel: React.FC<GuidedPromptPanelProps> = ({ process, applying, onStart }) => {
  const [values, setValues] = useState<Record<string, string | string[] | number>>({});
  const [prompt, setPrompt] = useState('');
  const [promptVisible, setPromptVisible] = useState(false);

  const fields = useMemo(() => process.fields || [], [process.fields]);

  const missingRequired = useMemo(
    () => fields.filter((field) => {
      if (!field.required) return false;
      const value = values[field.id];
      if (Array.isArray(value)) return value.length === 0;
      return value === undefined || value === null || !String(value).trim();
    }),
    [fields, values],
  );

  const handleGenerate = () => {
    if (missingRequired.length > 0) {
      message.warning(`请填写：${missingRequired.map((f) => f.label).join('、')}`);
      return;
    }
    const assembled = buildPromptFromTemplate(process, values);
    if (!assembled.trim()) {
      message.warning('无法生成提示词，请先完善选项');
      return;
    }
    setPrompt(assembled);
    setPromptVisible(true);
  };

  const handleStart = () => {
    if (!prompt.trim()) {
      message.warning('提示词为空');
      return;
    }
    onStart(prompt.trim());
  };

  return (
    <div className="ws-guided">
      <div className="ws-guided-head">
        <div className="ws-guided-icon">{process.icon || '✨'}</div>
        <div>
          <div className="ws-guided-title">{process.name}</div>
          {process.description && <div className="ws-guided-desc">{process.description}</div>}
        </div>
      </div>

      <div className="ws-guided-form">
        {fields.map((f) => (
          <div className="ws-field" key={f.id}>
            <label className="ws-field-label">
              {f.label}
              {f.required && <span className="ws-field-required">*</span>}
            </label>
            {f.type === 'select' ? (
              <Select
                className="ws-field-control"
                placeholder={`请选择${f.label}`}
                value={values[f.id] || undefined}
                onChange={(v) => setValues((s) => ({ ...s, [f.id]: v }))}
                options={(f.options || []).map((o) => ({ label: o, value: o }))}
                allowClear
              />
            ) : f.type === 'multiselect' ? (
              <Select
                mode="multiple"
                className="ws-field-control"
                placeholder={`请选择${f.label}`}
                value={(values[f.id] as string[] | undefined) || undefined}
                onChange={(v) => setValues((s) => ({ ...s, [f.id]: v }))}
                options={(f.options || []).map((o) => ({ label: o, value: o }))}
                allowClear
              />
            ) : f.type === 'number' ? (
              <InputNumber
                className="ws-field-control"
                placeholder={f.placeholder || `请输入${f.label}`}
                value={values[f.id] as number | undefined}
                onChange={(v) => setValues((s) => ({
                  ...s,
                  [f.id]: v === null ? '' : v,
                }))}
              />
            ) : (
              <Input
                className="ws-field-control"
                placeholder={f.placeholder || `请输入${f.label}`}
                value={(values[f.id] as string | undefined) || ''}
                onChange={(e) => setValues((s) => ({ ...s, [f.id]: e.target.value }))}
                allowClear
              />
            )}
          </div>
        ))}
      </div>

      <div className="ws-guided-actions">
        <Button
          type="default"
          icon={<ThunderboltOutlined />}
          onClick={handleGenerate}
          disabled={applying}
        >
          生成提示词
        </Button>
      </div>

      {promptVisible && (
        <div className="ws-guided-preview">
          <div className="ws-guided-preview-label">
            生成的提示词（可编辑）
          </div>
          <Input.TextArea
            className="ws-guided-preview-area"
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            autoSize={{ minRows: 4, maxRows: 12 }}
          />
          <Button
            type="primary"
            icon={<RocketOutlined />}
            loading={applying}
            onClick={handleStart}
            className="ws-guided-start"
          >
            开始对话
          </Button>
        </div>
      )}
    </div>
  );
};

export default GuidedPromptPanel;
