import { useLocation, useNavigate } from 'react-router-dom';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { useRunChatStore } from '@/stores/useRunChatStore';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import AgentCategoriesSidebar from './AgentCategoriesSidebar';
import AppCategoriesSidebar from './AppCategoriesSidebar';
import TemplateHistorySidebar from './TemplateHistorySidebar';
import AppHistorySidebar from './AppHistorySidebar';
import ProjectListSidebar from './ProjectListSidebar';
import ConversationHistory from '../ConversationHistory/ConversationHistory';
import './Sidebar.css';

const Sidebar = () => {
  const location = useLocation();
  const navigate = useNavigate();
  const path = location.pathname;

  const renderSidebar = () => {
    // Workspace routes share one history rail — the shell's "最近会话" (§19).
    // Selecting a conversation goes through `/?conversation=`, which resolves
    // the owning application before opening its workspace. The trailing slash
    // keeps `/apps` and `/workflows` (console pages) out of this branch.
    const isWorkspaceRoute = path === '/' || path === ''
      || path.startsWith('/chat/') || path.startsWith('/app/')
      || path.startsWith('/workflow/');
    if (isWorkspaceRoute) {
      return (
        <ConversationHistory
          activeConversationId={new URLSearchParams(location.search).get('conversation')}
          onConversationSelect={(id: string) => {
            navigate(id ? `/?conversation=${id}` : '/');
          }}
          onNewConversation={handleNewConversation}
        />
      );
    }
    if (path.startsWith('/agents')) {
      return <AgentCategoriesSidebar />;
    }
    if (path.startsWith('/templates')) {
      return <TemplateHistorySidebar />;
    }
    if (path === '/apps' || path === '/apps/') {
      // 应用中心：与智能体市场同构的分类栏。
      return <AppCategoriesSidebar />;
    }
    if (path.startsWith('/apps')) {
      return <AppHistorySidebar />;
    }
    if (path.startsWith('/workspace')) {
      return <ProjectListSidebar />;
    }
    return null;
  };

  // "新建" must do something visible from every workspace route: inside a chat
  // workspace it starts a NEW conversation of THAT agent (same as the header
  // 新建对话 button); from the home workspace it opens the main agent's chat
  // surface instead of a no-op navigate('/').
  //
  // The agent is looked up in the entity cache (populated by whatever route
  // the user is on) and then in the bootstrap payload — never by loading the
  // catalog (执行报告 §9).
  const handleNewConversation = () => {
    const entities = useApplicationEntityStore.getState();
    const activeApplicationId = useWorkspaceStore.getState().activeApplicationId;
    const slug = path.startsWith('/chat/')
      ? decodeURIComponent(path.slice('/chat/'.length))
      : null;
    const application = (slug ? entities.getBySlug(slug) : undefined)
      || entities.get(activeApplicationId)
      || useWorkspaceBootstrapStore.getState().defaultApplication;
    if (!application) {
      navigate('/');
      return;
    }
    useWorkspaceStore.getState().startNewConversation(application.id);
    useRunChatStore.getState().setActiveConversation(null);
    navigate(`/chat/${application.slug}`);
  };

  return (
    <aside className="app-sidebar">
      {renderSidebar()}
    </aside>
  );
};

export default Sidebar;
