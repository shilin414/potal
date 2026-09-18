/**
 * MobileCategoryRail — horizontally scrolling category pills (开发执行报告 §16).
 * Same visual language as the catalog sheet's tabs; `allLabel` pins the
 * leading "no filter" affordance that the data does not own.
 */
import React from 'react';

export interface MobileCategoryRailProps {
  categories: Array<{ slug: string; name: string }>;
  value: string;
  onChange: (slug: string) => void;
  allLabel?: string;
}

const MobileCategoryRail: React.FC<MobileCategoryRailProps> = ({
  categories, value, onChange, allLabel = '全部',
}) => (
  <div className="mobile-console-rail" role="tablist" aria-label="分类">
    <button
      type="button"
      role="tab"
      aria-selected={value === 'all'}
      className={`mobile-console-rail__pill${value === 'all' ? ' mobile-console-rail__pill--active' : ''}`}
      onClick={() => onChange('all')}
    >
      {allLabel}
    </button>
    {categories.map((tab) => (
      <button
        key={tab.slug}
        type="button"
        role="tab"
        aria-selected={value === tab.slug}
        className={`mobile-console-rail__pill${value === tab.slug ? ' mobile-console-rail__pill--active' : ''}`}
        onClick={() => onChange(tab.slug)}
      >
        {tab.name}
      </button>
    ))}
  </div>
);

export default MobileCategoryRail;
