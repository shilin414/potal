import { useMemo } from 'react';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import type { ApplicationSummary } from '@/services/runApi';

export function useRecentCapabilities(limit = 8): ApplicationSummary[] {
  const server = useWorkspaceBootstrapStore((state) => state.recentCapabilities);
  const localIds = useWorkspaceStore((state) => state.recentApplicationIds);
  const entities = useApplicationEntityStore((state) => state.byId);
  return useMemo(() => {
    const seen = new Set<number>();
    const merged: ApplicationSummary[] = [];
    for (const item of [...localIds.map((id) => entities[id]).filter(Boolean), ...server]) {
      if (!item || seen.has(item.id)) continue;
      seen.add(item.id);
      merged.push(item);
    }
    return merged.slice(0, limit);
  }, [entities, limit, localIds, server]);
}
