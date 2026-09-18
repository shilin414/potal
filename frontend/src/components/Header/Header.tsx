import React from 'react';
import { Dropdown, Avatar } from 'antd';
import { UserOutlined, LogoutOutlined, SettingOutlined } from '@ant-design/icons';
import { useNavigate, useLocation } from 'react-router-dom';
import { useAuthStore } from '@/stores/useAuthStore';
import { userAvatarFallback, userAvatarUrl, userDisplayName } from '@/lib/chatIdentity';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { ThemeToggle } from '@/components/Theme';
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
  const { user, logout, isAuthenticated } = useAuthStore();
  // The main agent comes from the bootstrap payload (执行报告 §9) — the
  // header must not ask for the whole catalog just to link to "对话".
  const defaultApplication = useWorkspaceBootstrapStore((state) => state.defaultApplication);
  const loadBootstrap = useWorkspaceBootstrapStore((state) => state.load);

  React.useEffect(() => { void loadBootstrap(); }, [loadBootstrap]);

  const handleLogout = async () => {
    await logout();
    navigate('/auth/login?logged_out=1', { replace: true });
  };

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

  const userMenuItems = [
    {
      key: 'profile',
      icon: <UserOutlined />,
      label: '个人中心',
      onClick: () => navigate('/profile'),
    },
    {
      key: 'settings',
      icon: <SettingOutlined />,
      label: '设置',
      onClick: () => navigate('/settings'),
    },
    { type: 'divider' as const },
    {
      key: 'logout',
      icon: <LogoutOutlined />,
      label: '登出',
      onClick: handleLogout,
    },
  ];

  const currentPath = location.pathname;

  return (
    <header className="app-header">
      {/* Left: Logo */}
      <div className="header-logo" onClick={() => navigate('/')}>
        Creation <span>Studio</span>
      </div>

      {/* Center: Nav */}
      <nav className="header-nav">
        {navItems.map((item) => (
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
        {isAuthenticated && (
          <Dropdown menu={{ items: userMenuItems }} placement="bottomRight">
            <div className="header-user">
              <div className="header-avatar">
                {userAvatarUrl(user) ? (
                  <Avatar size={32} src={userAvatarUrl(user)} />
                ) : (
                  userAvatarFallback(user)
                )}
              </div>
              <span className="header-username">{userDisplayName(user)}</span>
            </div>
          </Dropdown>
        )}
      </div>
    </header>
  );
};

export default Header;
