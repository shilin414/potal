import { Avatar, Button, Dropdown } from 'antd';
import { LogoutOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
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

  if (variant === 'panel') {
    return (
      <div className="account-menu-panel">
        <div className="account-menu-panel__identity">
          {avatar}
          <div>
            <div className="account-menu-panel__name">{userDisplayName(user)}</div>
            <div className="account-menu-panel__id">{userDisplayId(user) || user?.username || '—'}</div>
          </div>
        </div>
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
    );
  }

  return (
    <Dropdown
      placement="bottomRight"
      menu={{
        items: [{
          key: 'logout',
          icon: <LogoutOutlined />,
          label: isLoggingOut ? '退出中…' : '退出登录',
          disabled: isLoggingOut,
          onClick: () => void handleLogout(),
        }],
      }}
    >
      <div className="header-user">
        <div className="header-avatar">{avatar}</div>
        <span className="header-username">{userDisplayName(user)}</span>
      </div>
    </Dropdown>
  );
};

export default AccountMenu;
