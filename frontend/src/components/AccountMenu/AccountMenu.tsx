import { useState } from 'react';
import { Avatar, Button, Dropdown } from 'antd';
import { LogoutOutlined, SettingOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { NavigationSettingsModal } from '@/components/Navigation';
import {
  userAvatarFallback,
  userAvatarUrl,
  userDisplayId,
  userDisplayName,
} from '@/lib/chatIdentity';
import { useAuthStore } from '@/stores/useAuthStore';

interface AccountMenuProps {
  variant?: 'dropdown' | 'panel';
  onLogoutComplete?: () => void;
}

const AccountMenu: React.FC<AccountMenuProps> = ({
  variant = 'dropdown',
  onLogoutComplete,
}) => {
  const navigate = useNavigate();
  const [settingsOpen, setSettingsOpen] = useState(false);
  const user = useAuthStore((state) => state.user);
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);
  const isLoggingOut = useAuthStore((state) => state.isLoggingOut);
  const logout = useAuthStore((state) => state.logout);

  if (!isAuthenticated) return null;

  const handleLogout = async () => {
    if (isLoggingOut) return;
    await logout();
    onLogoutComplete?.();
    navigate('/auth/login?logged_out=1', { replace: true });
  };

  const avatar = (
    <Avatar size={32} src={userAvatarUrl(user) || undefined}>
      {userAvatarFallback(user)}
    </Avatar>
  );

  const settingsModal = (
    <NavigationSettingsModal
      open={settingsOpen}
      onClose={() => setSettingsOpen(false)}
      isStaff={Boolean(user?.is_staff)}
    />
  );

  if (variant === 'panel') {
    return (
      <>
        <div className="account-menu-panel">
          <div className="account-menu-panel__identity">
            {avatar}
            <div>
              <div className="account-menu-panel__name">{userDisplayName(user)}</div>
              <div className="account-menu-panel__id">{userDisplayId(user) || user?.username || '—'}</div>
            </div>
          </div>
          <div className="account-menu-panel__actions">
            <Button
              block
              icon={<SettingOutlined />}
              onClick={() => setSettingsOpen(true)}
            >
              界面设置
            </Button>
            <Button
              danger
              block
              icon={<LogoutOutlined />}
              loading={isLoggingOut}
              disabled={isLoggingOut}
              onClick={() => void handleLogout()}
            >
              {isLoggingOut ? '退出中…' : '退出登录'}
            </Button>
          </div>
        </div>
        {settingsModal}
      </>
    );
  }

  return (
    <>
      <Dropdown
        trigger={['click']}
        placement="bottomRight"
        menu={{
          items: [
            {
              key: 'interface-settings',
              icon: <SettingOutlined />,
              label: '导航与外观',
              onClick: () => setSettingsOpen(true),
            },
            { type: 'divider' },
            {
              key: 'logout',
              icon: <LogoutOutlined />,
              label: isLoggingOut ? '退出中…' : '退出登录',
              disabled: isLoggingOut,
              onClick: () => void handleLogout(),
            },
          ],
        }}
      >
        <button type="button" className="header-user" aria-label="打开账号菜单">
          <div className="header-avatar">{avatar}</div>
          <span className="header-username">{userDisplayName(user)}</span>
        </button>
      </Dropdown>
      {settingsModal}
    </>
  );
};

export default AccountMenu;
