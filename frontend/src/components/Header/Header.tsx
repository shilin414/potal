import React from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import AccountMenu from '@/components/AccountMenu/AccountMenu';
import {
  NavigationItemIcon,
  getVisibleNavigationItems,
  isNavigationItemActive,
  type NavigationItem,
} from '@/components/Navigation';
import { ThemeToggle } from '@/components/Theme';
import { useAuthStore } from '@/stores/useAuthStore';
import { useNavigationPreferencesStore } from '@/stores/useNavigationPreferencesStore';
import './Header.css';

const Header: React.FC = () => {
  const navigate = useNavigate();
  const location = useLocation();
  // The main agent comes from the bootstrap payload (执行报告 §9) — the
  // header must not ask for the whole catalog just to link to "对话".
  const isStaff = useAuthStore((state) => Boolean(state.user?.is_staff));
  const iconMode = useNavigationPreferencesStore((state) => state.iconMode);
  const visibleNavItems = getVisibleNavigationItems({ isStaff });


  const handleNavigation = (item: NavigationItem) => {
    navigate(item.path);
  };

  return (
    <header className="app-header">
      <button
        type="button"
        className="header-logo"
        onClick={() => navigate('/')}
      >
        Creation <span>Studio</span>
      </button>

      <nav className="header-nav" aria-label="主导航">
        {visibleNavItems.map((item) => {
          const active = isNavigationItemActive(item.id, location.pathname);
          return (
            <button
              key={item.id}
              type="button"
              className={`header-nav-item${active ? ' active' : ''}`}
              aria-current={active ? 'page' : undefined}
              onClick={() => handleNavigation(item)}
            >
              <NavigationItemIcon
                item={item}
                className={iconMode === 'emoji' ? 'header-nav-emoji' : 'header-nav-icon'}
              />
              <span>{item.desktopLabel}</span>
            </button>
          );
        })}
      </nav>

      <div className="header-right">
        <ThemeToggle />
        <AccountMenu />
      </div>
    </header>
  );
};

export default Header;
