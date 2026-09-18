import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { api } from '@/services/api';
import {
  captureSessionGeneration,
  registerSessionReset,
  sessionStillCurrent,
} from '@/stores/resetSessionState';

export interface Organization {
  id: string;
  name: string;
  slug: string;
  role: string;
}

interface OrganizationState {
  organizations: Organization[];
  currentOrganizationId: string | null;
  loadOrganizations: () => Promise<void>;
  selectOrganization: (id: string) => void;
  reset: () => void;
}

export const useOrganizationStore = create<OrganizationState>()(
  persist(
    (set, get) => ({
      organizations: [],
      currentOrganizationId: null,
      loadOrganizations: async () => {
        const generation = captureSessionGeneration();
        const response = await api.get<Organization[] | { results: Organization[] }>(
          '/enterprise/organizations/'
        );
        if (!sessionStillCurrent(generation)) return;
        const organizations = Array.isArray(response) ? response : response.results;
        const selected = get().currentOrganizationId;
        set({
          organizations,
          currentOrganizationId: organizations.some((item) => item.id === selected)
            ? selected
            : organizations[0]?.id ?? null,
        });
      },
      selectOrganization: (id) => set({ currentOrganizationId: id }),
      reset: () => set({ organizations: [], currentOrganizationId: null }),
    }),
    { name: 'organization-storage' }
  )
);

// Organizations and the selected organization id belong to ONE identity
// (三次复审 P0-R2): the axios interceptor reads the PERSISTED
// `organization-storage` key on every request and sends it as
// X-Organization-ID, so user A's selection must not survive into user B's
// session — B's first requests would otherwise carry A's organization id
// until B's own organization list finished loading.
//
// The persisted key itself is also wiped by resetSessionScopedState
// (SESSION_SCOPED_STORAGE_KEYS): this module can sit in a lazy chunk that a
// logout never imports, and the key removal is what stops zustand from
// hydrating A's organizations back into B's session on first load.
registerSessionReset(() => useOrganizationStore.getState().reset());
