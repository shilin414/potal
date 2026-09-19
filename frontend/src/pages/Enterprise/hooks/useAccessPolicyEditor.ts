/**
 * useAccessPolicyEditor — the ONE access-policy state machine shared by the
 * desktop AccessPage drawer and the MobilePermissionEditor (三次复审 P0).
 *
 * Desktop and Mobile used to duplicate load/error/policy/save/grant-mutation
 * logic — and BOTH carried the same asynchronous race: switching resources
 * while a slow `GET access(A)` was still travelling let A's policy land
 * after B's, so the page showed B's title over A's grants, and 保存 would
 * then write A's ACL onto B (`updateAccess(B, A_POLICY)`).
 *
 * The hook pins three layers of defense:
 *   · request generation guard — a stale target's response can never land
 *     (open/switch/close all invalidate in-flight generations);
 *   · `ready` business invariant — the editor is only ready when the loaded
 *     policy's `application_id` MATCHES the current target, and save refuses
 *     to run otherwise (even if the sequence guard were ever removed);
 *   · save target guard — a slow save for A that completes after the switch
 *     to B applies its response (and notifies) only while the editor still
 *     targets A, so it can never close/overwrite B's surface.
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { message } from 'antd';
import {
  enterpriseApi,
  type AccessMode,
  type AccessPolicy,
  type DirectoryDepartment,
} from '../enterpriseApi';

/** One granted-user row, in the shape both pickers produce. */
export interface AccessUserGrantInput {
  directory_user_id: number;
  name: string;
  avatar_url: string;
  departments: string[];
}

export interface UseAccessPolicyEditorOptions {
  /** The application whose ACL is being edited; null = closed. */
  applicationId: number | null;
  /** False suppresses (and invalidates) all loading — e.g. the drawer closed. */
  enabled: boolean;
  /** Runs after a SUCCESSFUL save that still targets the current application. */
  onSaved?: () => void;
}

export interface UseAccessPolicyEditorResult {
  policy: AccessPolicy | null;
  departments: DirectoryDepartment[];

  loading: boolean;
  loadError: string | null;
  saving: boolean;

  /** True only when a policy matching the CURRENT target is loaded and editable. */
  ready: boolean;

  reload(): Promise<void>;
  save(): Promise<boolean>;

  setAccessMode(mode: AccessMode): void;
  setDepartmentIds(ids: number[], defaultIncludeChildren?: boolean): void;
  patchDepartmentGrant(departmentId: number, includeChildren: boolean): void;
  setUserGrants(users: AccessUserGrantInput[]): void;
}

export function useAccessPolicyEditor({
  applicationId,
  enabled,
  onSaved,
}: UseAccessPolicyEditorOptions): UseAccessPolicyEditorResult {
  const [policy, setPolicy] = useState<AccessPolicy | null>(null);
  const [departments, setDepartments] = useState<DirectoryDepartment[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  // Request generation guard (P0-1/P0-2): every load bumps the sequence, and
  // a response only lands when it is still the newest generation. Closing the
  // editor or switching targets bumps it too, via the effect cleanup below.
  const requestSeqRef = useRef(0);
  // The LIVE target (P0-3): a save in flight compares against this ref, not
  // against its own closure, so switching targets mid-save is detected.
  const targetIdRef = useRef<number | null>(applicationId);
  useEffect(() => { targetIdRef.current = applicationId; }, [applicationId]);
  const onSavedRef = useRef(onSaved);
  useEffect(() => { onSavedRef.current = onSaved; });

  const reload = useCallback(async () => {
    if (!applicationId || !enabled) return;
    const seq = ++requestSeqRef.current;
    // A new load starts from a clean slate: switching A → B must never leave
    // A's grants on screen under B's title, even before the network answers.
    setPolicy(null);
    setDepartments([]);
    setLoading(true);
    setLoadError(null);
    try {
      const [p, d] = await Promise.all([
        enterpriseApi.access(applicationId),
        enterpriseApi.departments(),
      ]);
      if (seq !== requestSeqRef.current) return; // a newer generation won
      setPolicy(p);
      setDepartments(d);
    } catch {
      if (seq !== requestSeqRef.current) return;
      setLoadError('加载访问权限失败');
    } finally {
      if (seq === requestSeqRef.current) setLoading(false);
    }
  }, [applicationId, enabled]);

  // Load on open / target change. The cleanup invalidates whatever is still
  // in flight for the PREVIOUS target — closing A or switching to B must
  // abandon A's requests entirely (P0-2).
  useEffect(() => {
    if (enabled && applicationId) {
      void reload();
    }
    return () => {
      requestSeqRef.current += 1;
    };
  }, [enabled, applicationId, reload]);

  // P0-4 business invariant: even if every guard above were ever removed, a
  // policy that does not belong to the current target is never "ready" and
  // never enters save.
  const ready = Boolean(
    applicationId
    && !loading
    && !loadError
    && policy
    && policy.application_id === applicationId,
  );

  const save = useCallback(async (): Promise<boolean> => {
    if (!applicationId || !policy || policy.application_id !== applicationId) {
      return false;
    }
    const saveTargetId = applicationId;
    setSaving(true);
    try {
      const next = await enterpriseApi.updateAccess(saveTargetId, {
        access_mode: policy.access_mode,
        department_grants: policy.departments.map((d) => ({
          department_id: d.department_id,
          include_children: d.include_children,
        })),
        user_grants: policy.users.map((u) => u.directory_user_id),
      });
      // A slow save for A must not close B's editor or overwrite its policy
      // (P0-3): the response only applies while the target is unchanged.
      if (targetIdRef.current === saveTargetId) {
        setPolicy(next);
        onSavedRef.current?.();
      }
      return true;
    } catch {
      if (targetIdRef.current === saveTargetId) {
        message.error('保存失败，请检查部门和人员是否仍有效');
      }
      return false;
    } finally {
      if (targetIdRef.current === saveTargetId) setSaving(false);
    }
  }, [applicationId, policy]);

  const setAccessMode = useCallback((mode: AccessMode) => {
    setPolicy((prev) => (prev ? { ...prev, access_mode: mode } : prev));
  }, []);

  const setDepartmentIds = useCallback((ids: number[], defaultIncludeChildren = true) => {
    setPolicy((prev) => {
      if (!prev) return prev;
      const old = new Map(prev.departments.map((d) => [d.department_id, d]));
      return {
        ...prev,
        departments: ids.map((id) => old.get(id) || {
          department_id: id,
          name: departments.find((d) => d.id === id)?.name || '',
          include_children: defaultIncludeChildren,
          covered_users: 0,
        }),
      };
    });
  }, [departments]);

  const patchDepartmentGrant = useCallback((
    departmentId: number,
    includeChildren: boolean,
  ) => {
    setPolicy((prev) => (prev ? {
      ...prev,
      departments: prev.departments.map((d) => (
        d.department_id === departmentId
          ? { ...d, include_children: includeChildren }
          : d)),
    } : prev));
  }, []);

  const setUserGrants = useCallback((users: AccessUserGrantInput[]) => {
    setPolicy((prev) => {
      if (!prev) return prev;
      const old = new Map(prev.users.map((u) => [u.directory_user_id, u]));
      return {
        ...prev,
        users: users.map((u) => old.get(u.directory_user_id) || {
          directory_user_id: u.directory_user_id,
          name: u.name,
          avatar_url: u.avatar_url,
          departments: u.departments,
        }),
      };
    });
  }, []);

  return {
    policy,
    departments,
    loading,
    loadError,
    saving,
    ready,
    reload,
    save,
    setAccessMode,
    setDepartmentIds,
    patchDepartmentGrant,
    setUserGrants,
  };
}
