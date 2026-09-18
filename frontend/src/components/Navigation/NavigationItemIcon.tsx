import type { NavigationItem } from './navigationConfig';
import { getNavigationIconComponent } from './navigationIconComponents';
import { useNavigationPreferencesStore } from '@/stores/useNavigationPreferencesStore';

interface NavigationItemIconProps {
  item: NavigationItem;
  surface?: 'desktop' | 'mobile';
  className?: string;
}

const NavigationItemIcon: React.FC<NavigationItemIconProps> = ({
  item,
  surface = 'desktop',
  className,
}) => {
  const iconMode = useNavigationPreferencesStore((state) => state.iconMode);
  const preferredIcon = useNavigationPreferencesStore((state) => state.icons[item.id]);

  if (iconMode === 'hidden') return null;

  if (iconMode === 'emoji') {
    return <span className={className} aria-hidden="true">{item.emoji}</span>;
  }

  const fallbackIcon = surface === 'mobile'
    ? item.mobileDefaultIcon ?? item.defaultIcon
    : item.defaultIcon;
  const Icon = getNavigationIconComponent(preferredIcon ?? fallbackIcon);
  return <Icon className={className} aria-hidden="true" />;
};

export default NavigationItemIcon;
