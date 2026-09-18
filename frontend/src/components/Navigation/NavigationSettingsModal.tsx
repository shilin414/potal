import { Button, Modal, Segmented, Select } from 'antd';
import { UndoOutlined } from '@ant-design/icons';
import { getVisibleNavigationItems } from './navigationConfig';
import {
  NAVIGATION_ICON_OPTIONS,
  getNavigationIconComponent,
  type NavigationIconId,
} from './navigationIconComponents';
import {
  useNavigationPreferencesStore,
  type NavigationIconMode,
} from '@/stores/useNavigationPreferencesStore';
import './NavigationSettingsModal.css';

interface NavigationSettingsModalProps {
  open: boolean;
  onClose: () => void;
  isStaff: boolean;
}

const MODE_OPTIONS: Array<{ label: string; value: NavigationIconMode }> = [
  { label: '黑白线稿', value: 'outline' },
  { label: 'Emoji', value: 'emoji' },
  { label: '仅文字', value: 'hidden' },
];

const NavigationSettingsModal: React.FC<NavigationSettingsModalProps> = ({
  open,
  onClose,
  isStaff,
}) => {
  const iconMode = useNavigationPreferencesStore((state) => state.iconMode);
  const icons = useNavigationPreferencesStore((state) => state.icons);
  const setIconMode = useNavigationPreferencesStore((state) => state.setIconMode);
  const setIcon = useNavigationPreferencesStore((state) => state.setIcon);
  const resetIcons = useNavigationPreferencesStore((state) => state.resetIcons);

  return (
    <Modal
      title="导航与外观"
      open={open}
      onCancel={onClose}
      footer={null}
      width={560}
      destroyOnHidden
    >
      <div className="navigation-settings">
        <section className="navigation-settings__section" aria-labelledby="navigation-icon-mode">
          <div className="navigation-settings__heading" id="navigation-icon-mode">
            图标显示
          </div>
          <Segmented
            block
            options={MODE_OPTIONS}
            value={iconMode}
            onChange={(value) => setIconMode(value as NavigationIconMode)}
          />
        </section>

        <section className="navigation-settings__section" aria-labelledby="navigation-icon-customization">
          <div className="navigation-settings__heading" id="navigation-icon-customization">
            自定义图标
          </div>
          {iconMode === 'outline' ? (
            <>
              <div className="navigation-settings__rows">
                {getVisibleNavigationItems({ isStaff }).map((item) => {
                  const selectedIcon = icons[item.id] ?? item.defaultIcon;
                  return (
                    <label className="navigation-settings__row" key={item.id}>
                      <span>{item.desktopLabel}</span>
                      <Select<NavigationIconId>
                        aria-label={`${item.desktopLabel}图标`}
                        value={selectedIcon}
                        onChange={(value) => setIcon(item.id, value)}
                        options={NAVIGATION_ICON_OPTIONS.map((option) => {
                          const Icon = getNavigationIconComponent(option.id);
                          return {
                            value: option.id,
                            label: (
                              <span className="navigation-settings__option">
                                <Icon aria-hidden="true" />
                                <span>{option.label}</span>
                              </span>
                            ),
                          };
                        })}
                      />
                    </label>
                  );
                })}
              </div>
              <Button icon={<UndoOutlined />} onClick={resetIcons}>
                恢复默认图标
              </Button>
            </>
          ) : (
            <p className="navigation-settings__hint">
              {iconMode === 'emoji'
                ? '当前使用系统 Emoji 导航图标。'
                : '当前导航仅显示文字。'}
            </p>
          )}
        </section>
      </div>
    </Modal>
  );
};

export default NavigationSettingsModal;
