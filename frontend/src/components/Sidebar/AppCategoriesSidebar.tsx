import React, { useMemo } from 'react';
import { Menu } from 'antd';
import { useApplicationCatalogStore } from '@/stores/useApplicationCatalogStore';

/**
 * AppCategoriesSidebar — 应用中心的分类栏（与智能体市场的分类栏同构）。
 *
 * 分类直接从 v2 应用目录的固定应用里聚合，无需单独接口；选择状态放在
 * useApplicationCatalogStore 里，由侧栏与卡片网格共享。
 */
const AppCategoriesSidebar: React.FC = () => {
  const applications = useApplicationCatalogStore((state) => state.applications);
  const fixedCategory = useApplicationCatalogStore((state) => state.fixedCategory);
  const setFixedCategory = useApplicationCatalogStore((state) => state.setFixedCategory);

  const categories = useMemo(() => {
    const counts = new Map<string, { name: string; count: number }>();
    applications
      .filter((app) => app.kind !== 'chat')
      .forEach((app) => {
        const slug = app.category_slug || '';
        if (!slug) return;
        const entry = counts.get(slug) || { name: app.category_name || slug, count: 0 };
        entry.count += 1;
        counts.set(slug, entry);
      });
    return [...counts.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  }, [applications]);

  const menuItems = [
    { key: 'all', label: '全部应用' },
    ...categories.map(([slug, entry]) => ({
      key: slug,
      label: `${entry.name} (${entry.count})`,
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
