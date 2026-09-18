import { useEffect, useRef, useState } from 'react';
import { RouterProvider } from 'react-router-dom';
import router from './router';
import {
  bootstrapPersistedSession,
  bootstrapPersistedSessionOnce,
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
  const [sessionReady, setSessionReady] = useState(
    () => !needsInitialVerification.current,
  );

  useEffect(() => {
    let active = true;
    const settle = (verification: Promise<void>) => {
      const version = ++verificationVersion.current;
      setSessionReady(false);
      void verification.finally(() => {
        if (active && verificationVersion.current === version) {
          setSessionReady(true);
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
    return () => {
      active = false;
      unsubscribe();
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
