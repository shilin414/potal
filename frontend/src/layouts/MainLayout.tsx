import { ReactNode } from 'react';
import { useLocation } from 'react-router-dom';
import Header from '../components/Header/Header';
import Sidebar from '../components/Sidebar/Sidebar';

interface MainLayoutProps {
  children: ReactNode;
  /** Hide the left sidebar (header + main only), e.g. for a launched app view. */
  hideSidebar?: boolean;
  /** Hide the top header (nav + account) for a fullscreen launched-app view. */
  hideHeader?: boolean;
}

const MainLayout: React.FC<MainLayoutProps> = ({ children, hideSidebar, hideHeader }) => {
  const location = useLocation();
  // Chat page needs full-height content without padding
  const isChatPage = location.pathname === '/' || location.pathname === '';

  const layoutClass = [
    'app-layout',
    hideSidebar && 'app-layout--nosidebar',
    hideHeader && 'app-layout--noheader',
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div className={layoutClass}>
      {!hideHeader && <Header />}
      {!hideSidebar && <Sidebar />}
      <main className={`app-main ${isChatPage ? '' : 'app-main--padded'}`}>
        {children}
      </main>
    </div>
  );
};

export default MainLayout;
