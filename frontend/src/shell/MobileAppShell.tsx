import { Outlet, useLocation, useNavigate } from 'react-router-dom';
import { Button, Drawer } from 'antd';
import {
  ArrowLeftOutlined,
  MenuOutlined,
  MoreOutlined,
  PlusOutlined,
  SearchOutlined,
} from '@ant-design/icons';
import MobileAgentSwitcher from '@/components/Mobile/MobileAgentSwitcher';
import MobileWorkbenchDrawer from '@/workbench/shell/MobileWorkbenchDrawer';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { useRunChatStore } from '@/stores/useRunChatStore';
import type { ShellChrome } from './useShellChrome';
import { MobileHeaderProvider, useMobileHeaderState } from './mobileHeader';
import './shell.css';

const MobileShellContent: React.FC<{ chrome: ShellChrome }> = ({ chrome }) => {
  const mobileNavOpen = useWorkspaceStore((state) => state.mobileNavOpen);
  const setMobileNavOpen = useWorkspaceStore((state) => state.setMobileNavOpen);
  const navigate = useNavigate();
  const location = useLocation();
  const pageOverride = useMobileHeaderState();
  const path = location.pathname;
  const mobile = { ...chrome.mobile, ...pageOverride };
  const mode = mobile.mode ?? 'workspace';
  const showBack = mobile.showBack ?? mode === 'detail';
  const showMenu = mobile.showMenu ?? !showBack;

  const go = (targetPath: string) => {
    setMobileNavOpen(false);
    navigate(targetPath);
  };

  const handleNewConversation = () => {
    const entities = useApplicationEntityStore.getState();
    const activeApplicationId = useWorkspaceStore.getState().activeApplicationId;
    const slug = path.startsWith('/chat/')
      ? decodeURIComponent(path.slice('/chat/'.length))
      : null;
    const application = (slug ? entities.getBySlug(slug) : undefined)
      || entities.get(activeApplicationId)
      || useWorkspaceBootstrapStore.getState().defaultApplication;
    setMobileNavOpen(false);
    if (!application) {
      navigate('/');
      return;
    }
    useWorkspaceStore.getState().startNewConversation(application.id);
    useRunChatStore.getState().setActiveConversation(null);
    navigate(`/chat/${application.slug}`);
  };

  const renderAction = () => {
    const action = mobile.action ?? (mode === 'workspace' ? 'new-task' : 'none');
    if (action === 'none') return <span className="mobile-shell__bar-spacer" aria-hidden />;
    if (action === 'new-task') {
      return (
        <Button
          type="primary"
          size="small"
          icon={<PlusOutlined />}
          className="mobile-shell__task-btn"
          onClick={handleNewConversation}
        >
          新任务
        </Button>
      );
    }
    const labels = { create: '新建', search: '搜索', more: '更多' } as const;
    const icons = {
      create: <PlusOutlined />,
      search: <SearchOutlined />,
      more: <MoreOutlined />,
    } as const;
    return (
      <button
        type="button"
        className="mobile-shell__icon-btn"
        aria-label={labels[action]}
        onClick={pageOverride?.onAction}
        disabled={!pageOverride?.onAction}
      >
        {icons[action]}
      </button>
    );
  };

  return (
    <div className="mobile-shell">
      {!chrome.hideHeader && (
        <header className="mobile-shell__bar">
          {showBack ? (
            <button
              type="button"
              className="mobile-shell__icon-btn"
              aria-label="返回企业控制台"
              onClick={() => go(mobile.backTo ?? '/enterprise')}
            >
              <ArrowLeftOutlined />
            </button>
          ) : showMenu ? (
            <button
              type="button"
              className="mobile-shell__icon-btn"
              aria-label="打开导航"
              onClick={() => setMobileNavOpen(true)}
            >
              <MenuOutlined />
            </button>
          ) : <span className="mobile-shell__bar-spacer" aria-hidden />}

          {mode === 'workspace' ? (
            <MobileAgentSwitcher />
          ) : (
            <div className="mobile-shell__title" title={mobile.title}>{mobile.title}</div>
          )}
          {renderAction()}
        </header>
      )}

      {/* No `--padded` modifier: MobilePage owns its own padding (二次复审
          P3-4 — a class with no CSS behind it only misleads). */}
      <main className="mobile-shell__main">
        <Outlet />
      </main>

      <Drawer
        placement="left"
        open={mobileNavOpen}
        onClose={() => setMobileNavOpen(false)}
        width="82vw"
        title="Potal"
        rootClassName="mobile-shell__drawer"
        styles={{ body: { padding: 0, display: 'flex', flexDirection: 'column' } }}
      >
        <MobileWorkbenchDrawer close={() => setMobileNavOpen(false)} />
      </Drawer>
    </div>
  );
};

const MobileAppShell: React.FC<{ chrome: ShellChrome }> = ({ chrome }) => (
  <MobileHeaderProvider>
    <MobileShellContent chrome={chrome} />
  </MobileHeaderProvider>
);

export default MobileAppShell;
