import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { api } from '@/services/api';

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
        const response = await api.get<Organization[] | { results: Organization[] }>(
          '/enterprise/organizations/'
        );
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
