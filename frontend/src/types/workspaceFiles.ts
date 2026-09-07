export type WorkspacePreviewKind = 'directory' | 'text' | 'image' | 'binary';

export interface WorkspaceFileEntry {
  name: string;
  path: string;
  is_directory: boolean;
  depth: number;
  size: number;
  modified_at: string;
  mime_type: string;
  preview_kind: WorkspacePreviewKind;
}

export interface WorkspaceFileListing {
  working_directory: string;
  entries: WorkspaceFileEntry[];
  file_count: number;
  truncated: boolean;
}

export interface WorkspaceFilePreview {
  name: string;
  path: string;
  size: number;
  mime_type: string;
  preview_kind: Exclude<WorkspacePreviewKind, 'directory'>;
  truncated: boolean;
  content?: string;
  data_url?: string;
}
