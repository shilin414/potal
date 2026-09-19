export type TaskExecutionState = 'idle' | 'running' | 'error';

export interface TaskSummary {
  id: string;
  title: string;
  applicationId: number | null;
  applicationSlug?: string;
  applicationName?: string;
  applicationIcon?: string;
  applicationColor?: string;
  applicationKind?: string;
  preview?: string;
  previewRole?: string;
  createdAt: string;
  updatedAt: string;
  executionState: TaskExecutionState;
}

export interface TaskPage {
  items: TaskSummary[];
  nextCursor: string;
}
