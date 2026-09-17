import React from 'react';
import { Menu } from 'antd';
import { useCatalogUiStore } from '@/stores/useCatalogUiStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';

/**
 * AppCategoriesSidebar — 应用中心的分类栏（与智能体市场的分类栏同构）。
 *
 * The rail comes from the workspace bootstrap (执行报告 §11): the server
 * returns the non-chat category list WITH counts, so this component no longer
 * aggregates the whole catalog (which used to mean every visit to 应用中心
 * downloaded every application before the rail could render).
 *
 * The selected category is shared with the card grid through
 * `useCatalogUiStore` — UI state, deliberately not a data store.
 */
const AppCategoriesSidebar: React.FC = () => {
  const categories = useWorkspaceBootstrapStore((state) => state.appCategories);
  const loadBootstrap = useWorkspaceBootstrapStore((state) => state.load);
  const fixedCategory = useCatalogUiStore((state) => state.fixedCategory);
  const setFixedCategory = useCatalogUiStore((state) => state.setFixedCategory);

  React.useEffect(() => { void loadBootstrap(); }, [loadBootstrap]);

  // The server returns first-appearance (created_at) order; the rail keeps the
  // alphabetical order it always had. 其他 (the __uncategorized__ sentinel) is
  // pinned last, where the paged endpoint expects it.
  const sorted = React.useMemo(() => {
    const named = categories.filter((item) => item.slug !== '__uncategorized__');
    const rest = categories.filter((item) => item.slug === '__uncategorized__');
    return [
      ...[...named].sort((a, b) => a.slug.localeCompare(b.slug)),
      ...rest,
    ];
  }, [categories]);

  const menuItems = [
    { key: 'all', label: '全部应用' },
    ...sorted.map((item) => ({
      key: item.slug,
      label: `${item.name} (${item.count})`,
    })),
  ];

  return (
    <Menu
      mode="inline"
      selectedKeys={[fixedCategory || 'all']}
      items={menuItems}
      onClick={({ key }) => setFixedCategory(key === 'all' ? null : key)}
    />
  );
};

export default AppCategoriesSidebar;
