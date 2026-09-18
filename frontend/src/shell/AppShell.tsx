import DesktopAppShell from './DesktopAppShell';
import MobileAppShell from './MobileAppShell';
import { useIsMobile } from './useIsMobile';
import { useShellChrome } from './useShellChrome';

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

  return isMobile
    ? <MobileAppShell chrome={chrome} />
    : <DesktopAppShell chrome={chrome} />;
};

export default AppShell;
