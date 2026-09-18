import { useEffect, useRef, useState } from 'react';
import { RouterProvider } from 'react-router-dom';
import router from './router';
import {
  bootstrapPersistedSession,
  bootstrapPersistedSessionOnce,
  syncSessionUser,
} from '@/services/session';
import { subscribeIdentityChange } from '@/stores/authBoundary';
import { useAuthStore } from '@/stores/useAuthStore';

function App() {
  // Persisted auth is only a convenience for restoring the shell; the
  // HttpOnly session cookie remains authoritative. Hold the private router
  // behind a verification boundary so a previous administrator's localStorage
  // cannot flash Enterprise navigation or private workspace state.
  const needsInitialVerification = useRef((() => {
    const auth = useAuthStore.getState();
    return auth.isAuthenticated && Boolean(auth.user);
  })());
  const verificationVersion = useRef(0);
  const verificationPending = useRef(false);
  const foregroundRefreshRunning = useRef(false);
  const foregroundRefreshQueued = useRef(false);
  const [sessionReady, setSessionReady] = useState(
    () => !needsInitialVerification.current,
  );

  useEffect(() => {
    let active = true;
    const settle = (verification: Promise<void>) => {
      const version = ++verificationVersion.current;
      verificationPending.current = true;
      setSessionReady(false);
      void verification.finally(() => {
        if (verificationVersion.current === version) {
          verificationPending.current = false;
          if (active) setSessionReady(true);
        }
      });
    };

    if (needsInitialVerification.current) {
      settle(bootstrapPersistedSessionOnce());
    }

    const unsubscribe = subscribeIdentityChange((userId) => {
      const currentUserId = useAuthStore.getState().user?.id;
      if (userId && currentUserId != null && String(currentUserId) === userId) {
        return;
      }
      settle(bootstrapPersistedSession());
    });
    const refreshCurrentSession = () => {
      if (verificationPending.current) return;
      if (foregroundRefreshRunning.current) {
        foregroundRefreshQueued.current = true;
        return;
      }
      const run = async () => {
        foregroundRefreshRunning.current = true;
        try {
          do {
            foregroundRefreshQueued.current = false;
            await syncSessionUser();
          } while (
            foregroundRefreshQueued.current
            && !verificationPending.current
          );
        } finally {
          foregroundRefreshRunning.current = false;
        }
      };
      void run();
    };
    const refreshVisibleSession = () => {
      if (document.visibilityState === 'visible') refreshCurrentSession();
    };
    window.addEventListener('focus', refreshCurrentSession);
    document.addEventListener('visibilitychange', refreshVisibleSession);
    return () => {
      active = false;
      unsubscribe();
      window.removeEventListener('focus', refreshCurrentSession);
      document.removeEventListener('visibilitychange', refreshVisibleSession);
    };
  }, []);

  if (!sessionReady) {
    return (
      <div style={{ padding: 32, color: 'var(--color-text-secondary, #666)' }}>
        正在验证登录状态…
      </div>
    );
  }

  return <RouterProvider router={router} />;
}

export default App;
