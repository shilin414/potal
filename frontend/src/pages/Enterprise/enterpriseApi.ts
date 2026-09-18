import { api } from "@/services/api";

export type AccessMode = "all" | "assigned" | "admin_only";
export interface DirectoryDepartment {
  id: number;
  open_department_id: string;
  name: string;
  parent_id?: number | null;
  parent_open_department_id: string;
  order_weight: string;
  is_active: boolean;
  last_synced_at?: string | null;
}
export interface DirectoryUser {
  id: number;
  open_id: string;
  name: string;
  avatar_url: string;
  active_status: number;
  is_resigned: boolean;
  local_user_id?: number | null;
  is_active: boolean;
  departments: Array<{ id: number; name: string; is_primary: boolean }>;
}
export interface DirectoryUserPage {
  results: DirectoryUser[];
  next_cursor?: string | null;
}
export interface SyncConfig {
  enabled: boolean;
  schedule_type: "interval" | "daily";
  interval_minutes: number;
  daily_time: string;
  timezone: string;
  next_run_at?: string | null;
  last_run_at?: string | null;
  last_success_at?: string | null;
  updated_at: string;
}
export interface SyncRun {
  id: number;
  trigger_type: "manual" | "scheduled";
  status: "pending" | "running" | "success" | "failed";
  departments_count: number;
  users_count: number;
  active_users_count: number;
  memberships_count: number;
  active_memberships_count: number;
  started_at?: string | null;
  finished_at?: string | null;
  error_code: string;
  error_message: string;
  created_at: string;
}
export interface AccessPolicy {
  application_id: number;
  access_mode: AccessMode;
  departments: Array<{
    department_id: number;
    name: string;
    include_children: boolean;
    covered_users: number;
  }>;
  users: Array<{
    directory_user_id: number;
    name: string;
    avatar_url: string;
    departments: string[];
  }>;
}
export interface DirectoryStats {
  departments_total: number;
  departments_active: number;
  users_total: number;
  users_active: number;
  users_resigned: number;
  oauth_users: number;
  linked_directory_users: number;
}
export interface AuditLog {
  id: number;
  user_id?: number | null;
  action: string;
  resource_type: string;
  resource_id: string;
  detail: Record<string, unknown>;
  created_at: string;
}

export const enterpriseApi = {
  stats: () => api.get<DirectoryStats>("/v2/admin/directory/stats"),
  departments: (params?: Record<string, unknown>) =>
    api.get<DirectoryDepartment[]>("/v2/admin/directory/departments", params),
  users: (params?: Record<string, unknown>) =>
    api.get<DirectoryUserPage>("/v2/admin/directory/users", params),
  syncConfig: () => api.get<SyncConfig>("/v2/admin/directory/sync-config"),
  updateSyncConfig: (data: Partial<SyncConfig>) =>
    api.put<SyncConfig>("/v2/admin/directory/sync-config", data),
  triggerSync: () => api.post<SyncRun>("/v2/admin/directory/sync"),
  syncRuns: (limit = 50) =>
    api.get<SyncRun[]>("/v2/admin/directory/sync-runs", { limit }),
  access: (id: number) =>
    api.get<AccessPolicy>(`/v2/admin/applications/${id}/access`),
  updateAccess: (
    id: number,
    data: {
      access_mode: AccessMode;
      department_grants: Array<{
        department_id: number;
        include_children: boolean;
      }>;
      user_grants: number[];
    },
  ) => api.put<AccessPolicy>(`/v2/admin/applications/${id}/access`, data),
  audits: (limit = 100) =>
    api.get<AuditLog[]>("/v2/admin/audit-logs", { limit }),
};
