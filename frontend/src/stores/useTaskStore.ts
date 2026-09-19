import { create } from 'zustand';
import { deleteTask, fetchTasks, renameTask, type TaskQuery } from '@/services/taskApi';
import type { TaskSummary } from '@/types/task';
import { captureSessionGeneration, registerSessionReset, sessionStillCurrent } from './resetSessionState';

interface TaskState {
  items: TaskSummary[];
  nextCursor: string;
  loading: boolean;
  error: string | null;
  query: Omit<TaskQuery, 'cursor'>;
  load: (query?: Omit<TaskQuery, 'cursor'>) => Promise<void>;
  loadMore: () => Promise<void>;
  rename: (id: string, title: string) => Promise<void>;
  remove: (id: string) => Promise<void>;
  replaceRecent: (items: TaskSummary[]) => void;
  clear: () => void;
}

const initial = {
  items: [] as TaskSummary[],
  nextCursor: '',
  loading: false,
  error: null as string | null,
  query: { limit: 20 } as Omit<TaskQuery, 'cursor'>,
};

export const useTaskStore = create<TaskState>((set, get) => ({
  ...initial,
  load: async (query = get().query) => {
    const generation = captureSessionGeneration();
    set({ loading: true, error: null, query });
    try {
      const page = await fetchTasks(query);
      if (!sessionStillCurrent(generation)) return;
      set({ items: page.items, nextCursor: page.nextCursor, loading: false });
    } catch {
      if (!sessionStillCurrent(generation)) return;
      set({ error: '任务加载失败', loading: false });
    }
  },
  loadMore: async () => {
    const { loading, nextCursor, query, items } = get();
    if (loading || !nextCursor) return;
    const generation = captureSessionGeneration();
    set({ loading: true, error: null });
    try {
      const page = await fetchTasks({ ...query, cursor: nextCursor });
      if (!sessionStillCurrent(generation)) return;
      const seen = new Set(items.map((item) => item.id));
      set({
        items: [...items, ...page.items.filter((item) => !seen.has(item.id))],
        nextCursor: page.nextCursor,
        loading: false,
      });
    } catch {
      if (!sessionStillCurrent(generation)) return;
      set({ error: '更多任务加载失败', loading: false });
    }
  },
  rename: async (id, title) => {
    const generation = captureSessionGeneration();
    const updated = await renameTask(id, title);
    if (!sessionStillCurrent(generation)) return;
    set((state) => ({ items: state.items.map((item) => item.id === id ? updated : item) }));
  },
  remove: async (id) => {
    const generation = captureSessionGeneration();
    await deleteTask(id);
    if (!sessionStillCurrent(generation)) return;
    set((state) => ({ items: state.items.filter((item) => item.id !== id) }));
  },
  replaceRecent: (items) => set({ items }),
  clear: () => set({ ...initial }),
}));

registerSessionReset(() => useTaskStore.getState().clear());
