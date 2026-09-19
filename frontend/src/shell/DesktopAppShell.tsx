import { Outlet } from 'react-router-dom';
import DesktopSidebar from '@/workbench/shell/DesktopSidebar';
import InspectorHost from '@/workbench/inspector/InspectorHost';
import type { ShellChrome } from './useShellChrome';

const DesktopAppShell: React.FC<{ chrome: ShellChrome }> = ({ chrome }) => (
  <div className={`desktop-workbench${chrome.hideSidebar ? ' desktop-workbench--fullscreen' : ''}`}>
    {!chrome.hideSidebar && <DesktopSidebar />}
    <main className={`app-main ${chrome.padded ? 'app-main--padded' : ''}`}><Outlet /></main>
    {!chrome.hideSidebar && <InspectorHost />}
  </div>
);

export default DesktopAppShell;
