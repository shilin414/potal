import { describe, expect, it } from 'vitest';
import { buildPromptFromTemplate } from '../templateWorkflow';
import type { WorkflowProcess } from '@/types/workflow';

describe('buildPromptFromTemplate', () => {
  const process: WorkflowProcess = {
    id: 'brief',
    name: 'Brief',
    mode: 'guided',
    prompt_template: '主题：{topic}\n标签：{tags}\n数量：{count}',
  };

  it('supports text, multiple choices and number answers', () => {
    expect(buildPromptFromTemplate(process, {
      topic: '咖啡馆',
      tags: ['安静', '复古'],
      count: 3,
    })).toBe('主题：咖啡馆\n标签：安静、复古\n数量：3');
  });
});
