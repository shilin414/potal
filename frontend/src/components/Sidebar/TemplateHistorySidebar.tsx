import React, { useEffect } from 'react';
import { Menu } from 'antd';
import { useTemplateStore } from '@/stores/useTemplateStore';

const TemplateHistorySidebar: React.FC = () => {
  const { categories, selectedCategory, loadCategories, selectCategory } = useTemplateStore();

  useEffect(() => {
    loadCategories();
  }, [loadCategories]);

  const menuItems = [
    { key: 'all', label: '全部案例' },
    ...categories.map((category) => ({
      key: category.slug,
      label: `${category.name} (${category.template_count})`,
    })),
  ];

  return (
    <div className="tpl-sidebar">
      <div className="tpl-sidebar-section">
        <div className="sidebar-title">案例分类</div>
        <Menu
          mode="inline"
          selectedKeys={[selectedCategory || 'all']}
          items={menuItems}
          onClick={({ key }) => selectCategory(key === 'all' ? null : key)}
        />
      </div>
    </div>
  );
};

export default TemplateHistorySidebar;
