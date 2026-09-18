import React from 'react';

import { useNavigate, useLocation } from 'react-router-dom';
import AccountMenu from '@/components/AccountMenu/AccountMenu';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { ThemeToggle } from '@/components/Theme';
import { useAuthStore } from '@/stores/useAuthStore';
import './Header.css';

// 技能 / 案例库 / 工作流三个入口已从主页导航下掉（页面、路由与数据都保留，
// 仍可直接访问 /skills、/templates、/workflows）。恢复时把对应项加回数组即可。
const navItems = [
  { key: '/', label: '💬 对话' },
  { key: '/agents', label: '🤖 智能体' },
  { key: '/apps', label: '🧩 应用' },
  { key: '/schedules', label: '⏰ 定时任务' },
  { key: '/enterprise', label: '🏢 企业控制台' },
];

const Header: React.FC = () => {
  const navigate = useNavigate();
  const location = useLocation();
  // The main agent comes from the bootstrap payload (执行报告 §9) — the
  // header must not ask for the whole catalog just to link to "对话".
  const defaultApplication = useWorkspaceBootstrapStore((state) => state.defaultApplication);
  const loadBootstrap = useWorkspaceBootstrapStore((state) => state.load);
  const isStaff = useAuthStore((state) => Boolean(state.user?.is_staff));
  const visibleNavItems = navItems.filter((item) => item.key !== '/enterprise' || isStaff);

  React.useEffect(() => { void loadBootstrap(); }, [loadBootstrap]);

  const handleNav = (key: string) => {
    // "对话" is the chat surface, not the idle home: open the main agent's
    // workspace so the switcher / new-conversation header is available. Falls
    // back to the home workspace when no bound chat application exists yet.
    if (key === '/') {
      navigate(defaultApplication ? `/chat/${defaultApplication.slug}` : '/');
      return;
    }
    navigate(key);
  };
  const currentPath = location.pathname;

  return (
    <header className="app-header">
      {/* Left: Logo */}
      <div className="header-logo" onClick={() => navigate('/')}>
        Creation <span>Studio</span>
      </div>

      {/* Center: Nav */}
      <nav className="header-nav">
        {visibleNavItems.map((item) => (
          <div
            key={item.key}
            className={`header-nav-item ${
              currentPath === item.key || (item.key !== '/' && currentPath.startsWith(item.key))
                ? 'active' : ''}`}
            onClick={() => handleNav(item.key)}
          >
            {item.label}
          </div>
        ))}
      </nav>

      {/* Right: User */}
      <div className="header-right">
        <ThemeToggle />
        <AccountMenu />
      </div>
    </header>
  );
};

export default Header;
