import { useCallback, useEffect, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { api } from '@/services/api';
import { useProjectStore } from '@/stores/useProjectStore';
import type { ProjectAsset } from '@/types/workflow';

interface AppLike {
  name: string;
  description?: string;
  applicationId?: number;
}

/**
 * Lazily provisions a workspace (Project) for an app run.
 *
 * - If the URL already carries `?workspace=<id>` (e.g. reopened from history),
 *   that workspace is reused.
 * - Otherwise a new project is created (named after the app) on first mount and
 *   the id is written back into the URL (replace) so a refresh keeps the same
 *   workspace instead of spawning another.
 *
 * Exposes the workspace's assets plus an `addImageAsset` helper that persists a
 * generated file URL and refreshes the list.
 */
export function useAppWorkspace(app: AppLike | null, providedProjectId?: number | null) {
  const [searchParams, setSearchParams] = useSearchParams();
  const createProject = useProjectStore((s) => s.createProject);

  const workspaceParam = searchParams.get('workspace');
  const parsedWorkspaceId = workspaceParam ? Number(workspaceParam) : null;
  const urlProjectId = parsedWorkspaceId && Number.isInteger(parsedWorkspaceId)
    && parsedWorkspaceId > 0
    ? parsedWorkspaceId
    : null;
  const [projectId, setProjectId] = useState<number | null>(
    providedProjectId ?? urlProjectId,
  );
  const [assets, setAssets] = useState<ProjectAsset[]>([]);
  const [workspaceError, setWorkspaceError] = useState<string | null>(null);
  const creatingRef = useRef(false);

  const loadAssets = useCallback(async (id: number) => {
    try {
      const proj = await api.get<any>(`/projects/${id}/`);
      setAssets(proj.assets ?? []);
    } catch (error) {
      console.error('Failed to load workspace assets:', error);
    }
  }, []);

  // The runtime route stays mounted when only `?workspace=` changes. Keep the
  // internal project in sync so selecting another history item cannot reuse
  // the previous workspace and conversation.
  useEffect(() => {
    const requestedProjectId = providedProjectId ?? urlProjectId;
    if (requestedProjectId && requestedProjectId !== projectId) {
      creatingRef.current = false;
      setAssets([]);
      setWorkspaceError(null);
      setProjectId(requestedProjectId);
    }
  }, [projectId, providedProjectId, urlProjectId]);

  // Ensure a workspace exists.
  useEffect(() => {
    const requestedProjectId = providedProjectId ?? urlProjectId;
    if (requestedProjectId && requestedProjectId !== projectId) {
      // The synchronization effect above owns this transition. Do not issue a
      // stale asset request for the workspace that is being replaced.
      return;
    }
    if (projectId) {
      loadAssets(projectId);
      return;
    }
    if (!app) return;
    if (creatingRef.current) return;
    creatingRef.current = true;
    setWorkspaceError(null);
    createProject({
      title: app.name,
      description: app.description ?? '',
      application_id: app.applicationId,
    })
      .then((project) => {
        const id = project.id;
        setProjectId(id);
        setSearchParams({ workspace: String(id) }, { replace: true });
      })
      .catch((error) => {
        creatingRef.current = false;
        setWorkspaceError('创建应用工作目录失败');
        console.error('Failed to create app workspace:', error);
      });
  }, [app, projectId, providedProjectId, urlProjectId, loadAssets, createProject,
    setSearchParams]);

  /** Persist a generated image URL to the workspace and refresh the list. */
  const addImageAsset = useCallback(
    async (url: string, name?: string) => {
      if (!projectId || !url) return;
      try {
        await api.post(`/projects/${projectId}/assets/`, {
          asset_type: 'image',
          name: name || '生成图片',
          url,
        });
        await loadAssets(projectId);
      } catch (error) {
        console.error('Failed to save image to workspace:', error);
      }
    },
    [projectId, loadAssets],
  );

  const refresh = useCallback(() => {
    if (projectId) loadAssets(projectId);
  }, [projectId, loadAssets]);

  return {
    projectId,
    assets,
    addImageAsset,
    refresh,
    workspaceError,
    isWorkspaceReady: projectId !== null,
  };
}
