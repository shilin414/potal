export type WorkflowProcessMode = 'guided' | 'free';
export type WorkflowFieldType = 'select' | 'multiselect' | 'text' | 'number' | 'file';

export interface WorkflowField {
  id: string;
  label: string;
  type: WorkflowFieldType;
  options?: string[];
  placeholder?: string;
  required?: boolean;
}

export interface WorkflowProcess {
  id: string;
  name: string;
  icon?: string;
  description?: string;
  mode: WorkflowProcessMode;
  agent_slug?: string;
  prompt_template?: string;
  output_type?: string;
  fields?: WorkflowField[];
}

export interface WorkflowStructure {
  processes?: WorkflowProcess[];
  [key: string]: unknown;
}

export interface ProjectAsset {
  id: number;
  asset_type: 'video' | 'audio' | 'image' | 'text' | 'other';
  name: string;
  url?: string;
  content?: string;
  metadata?: Record<string, unknown>;
  order?: number;
  created_at?: string;
}

export interface WorkspaceProject {
  id: number;
  title: string;
  description?: string;
  structure: WorkflowStructure;
  status?: string;
  thumbnail?: string;
  assets?: ProjectAsset[];
  created_at?: string;
  updated_at?: string;
}
