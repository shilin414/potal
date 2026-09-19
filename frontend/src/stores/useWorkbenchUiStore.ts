import { create } from 'zustand';
import { persist } from 'zustand/middleware';

interface WorkbenchUiState {
  sidebarCollapsed: boolean;
  inspectorOpen: boolean;
  inspectorTab: 'task' | 'run' | 'artifacts' | 'context' | 'app';
  setSidebarCollapsed: (collapsed: boolean) => void;
  toggleSidebar: () => void;
  openInspector: (tab?: WorkbenchUiState['inspectorTab']) => void;
  closeInspector: () => void;
}

export const useWorkbenchUiStore = create<WorkbenchUiState>()(persist((set) => ({
  sidebarCollapsed: false,
  inspectorOpen: false,
  inspectorTab: 'task',
  setSidebarCollapsed: (sidebarCollapsed) => set({ sidebarCollapsed }),
  toggleSidebar: () => set((state) => ({ sidebarCollapsed: !state.sidebarCollapsed })),
  openInspector: (inspectorTab = 'task') => set({ inspectorOpen: true, inspectorTab }),
  closeInspector: () => set({ inspectorOpen: false }),
}), { name: 'potal-workbench-ui-v2' }));
