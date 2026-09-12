import { Outlet } from 'react-router-dom';
import Header from '@/components/Header/Header';
import Sidebar from '@/components/Sidebar/Sidebar';
import type { ShellChrome } from './useShellChrome';

/**
 * DesktopAppShell — the persistent PC shell (§19).
 *
 * It is a *layout* route: navigating between workspaces and console pages only
 * swaps <Outlet/>, so the header/sidebar (and any workspace kept alive inside)
 * never unmount (§33).
 */
const DesktopAppShell: React.FC<{ chrome: ShellChrome }> = ({ chrome }) => {
  const layoutClass = [
    'app-layout',
    chrome.hideSidebar && 'app-layout--nosidebar',
    chrome.hideHeader && 'app-layout--noheader',
  ].filter(Boolean).join(' ');

  return (
    <div className={layoutClass}>
      {!chrome.hideHeader && <Header />}
      {!chrome.hideSidebar && <Sidebar />}
      <main className={`app-main ${chrome.padded ? 'app-main--padded' : ''}`}>
        <Outlet />
      </main>
    </div>
  );
};

export default DesktopAppShell;
