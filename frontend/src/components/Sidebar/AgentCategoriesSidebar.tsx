import React, { useEffect } from 'react';
import { Menu } from 'antd';
import { useAgentStore } from '@/stores/useAgentStore';

const AgentCategoriesSidebar: React.FC = () => {
  const { categories, selectedCategory, loadCategories, selectCategory } = useAgentStore();

  useEffect(() => {
    loadCategories();
  }, [loadCategories]);

  const menuItems = [
    { key: 'all', label: '全部智能体' },
    ...categories.map((cat) => ({
      key: cat.slug,
      label: `${cat.name} (${cat.agent_count})`,
    })),
  ];

  return (
    <Menu
      mode="inline"
      selectedKeys={[selectedCategory || 'all']}
      items={menuItems}
      onClick={({ key }) => selectCategory(key === 'all' ? null : key)}
    />
  );
};

export default AgentCategoriesSidebar;
