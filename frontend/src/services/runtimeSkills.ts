import { api } from './api';

export type SkillProvider = 'codex' | 'graphflow';

export interface RuntimeSkillRoot {
  provider: SkillProvider;
  label: string;
  path: string;
  exists: boolean;
  skillCount: number;
}

export interface RuntimeSkillFile {
  path: string;
  size: number;
}

export interface RuntimeSkillSummary {
  provider: SkillProvider;
  providerLabel: string;
  slug: string;
  name: string;
  description: string;
  path: string;
  entrypoint: string;
  contentHash: string;
  updatedAt: string;
  fileCount: number;
}

export interface RuntimeSkillDetail extends RuntimeSkillSummary {
  content: string;
  files: RuntimeSkillFile[];
  canManage?: boolean;
}

export interface RuntimeSkillListResponse {
  roots: RuntimeSkillRoot[];
  skills: RuntimeSkillSummary[];
  canManage: boolean;
}

const detailPath = (provider: SkillProvider, slug: string) =>
  `/apps/runtime-skills/${encodeURIComponent(provider)}/${encodeURIComponent(slug)}/`;

export const runtimeSkillApi = {
  list: () => api.get<RuntimeSkillListResponse>('/apps/runtime-skills/'),
  get: (provider: SkillProvider, slug: string) =>
    api.get<RuntimeSkillDetail>(detailPath(provider, slug)),
  create: (data: {
    provider: SkillProvider;
    slug: string;
    name?: string;
    description?: string;
    content?: string;
  }) => api.post<RuntimeSkillDetail>('/apps/runtime-skills/', data),
  update: (provider: SkillProvider, slug: string, content: string) =>
    api.patch<RuntimeSkillDetail>(detailPath(provider, slug), { content }),
  remove: (provider: SkillProvider, slug: string) =>
    api.delete<void>(detailPath(provider, slug)),
};
