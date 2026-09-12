import { useEffect } from 'react';
import DesktopAppShell from './DesktopAppShell';
import MobileAppShell from './MobileAppShell';
import { useIsMobile } from './useIsMobile';
import { useShellChrome } from './useShellChrome';
import { syncSessionUser } from '@/services/session';

/**
 * AppShell — the single, always-mounted outer shell (§17/§33).
 *
 * One React tree serves PC and Mobile (§24): only the interaction layout is
 * swapped (DesktopAppShell / MobileAppShell), never the project, the API layer
 * or the WorkspaceHost inside it.
 */
const AppShell: React.FC = () => {
  const isMobile = useIsMobile();
  const chrome = useShellChrome();

  // The auth store is persisted in localStorage, so a session restored from a
  // previous build lacks the fields added since (display_name / display_id /
  // avatar_url). Refresh it once per shell mount — the shell never unmounts, so
  // this is one request per app load, and it keeps 姓名（user_id）accurate after
  // an operator changes the display id.
  useEffect(() => { void syncSessionUser(); }, []);

  return isMobile
    ? <MobileAppShell chrome={chrome} />
    : <DesktopAppShell chrome={chrome} />;
};

export default AppShell;
