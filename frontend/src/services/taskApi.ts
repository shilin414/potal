import { api } from './api';
import type { TaskPage, TaskSummary } from '@/types/task';

interface TaskSummaryPayload {
  id: string;
  title: string;
  application_id: number | null;
  application_slug?: string;
  application_name?: string;
  application_icon?: string;
  application_color?: string;
  application_kind?: string;
  preview?: string;
  preview_role?: string;
  created_at: string;
  updated_at: string;
  execution_state: TaskSummary['executionState'];
}

interface TaskPagePayload {
  items: TaskSummaryPayload[];
  next_cursor: string;
}

export interface TaskQuery {
  cursor?: string;
  limit?: number;
  q?: string;
  applicationId?: number;
}

export function adaptTask(payload: TaskSummaryPayload): TaskSummary {
  return {
    id: payload.id,
    title: payload.title,
    applicationId: payload.application_id,
    applicationSlug: payload.application_slug,
    applicationName: payload.application_name,
    applicationIcon: payload.application_icon,
    applicationColor: payload.application_color,
    applicationKind: payload.application_kind,
    preview: payload.preview,
    previewRole: payload.preview_role,
    createdAt: payload.created_at,
    updatedAt: payload.updated_at,
    executionState: payload.execution_state,
  };
}

export async function fetchTasks(query: TaskQuery = {}): Promise<TaskPage> {
  const payload = await api.get<TaskPagePayload>('/v2/tasks', {
    cursor: query.cursor || undefined,
    limit: query.limit,
    q: query.q?.trim() || undefined,
    application_id: query.applicationId,
  });
  return { items: payload.items.map(adaptTask), nextCursor: payload.next_cursor };
}

export async function renameTask(id: string, title: string): Promise<TaskSummary> {
  return adaptTask(await api.patch<TaskSummaryPayload>(`/v2/tasks/${id}`, { title }));
}

export function deleteTask(id: string): Promise<void> {
  return api.delete<void>(`/v2/tasks/${id}`);
}
