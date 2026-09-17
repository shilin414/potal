/**
 * MobileCatalogSheet — the mobile 智能体 / 应用 picker (design report §4–§6).
 *
 * ONE component serves both pickers and every entry point (home 双入口, the
 * chat top bar, the desktop switcher's mobile counterpart), because the report
 * is explicit that they are two *catalogue entries*, not two content panes
 * (§4.1): the difference is only which slice of the catalog shows and what the
 * title says.
 *
 * What it deliberately is NOT:
 *   · not a route — picking an item returns the application to the caller,
 *     which decides where to navigate. The sheet stays provider- and
 *     router-agnostic;
 *   · not a management surface — Provider / Runtime / 使用次数 / 编辑 / 删除
 *     stay in 智能体市场 (§5.5);
 *   · not the desktop Dropdown — that is `ApplicationSwitcher`, untouched.
 *
 * Search is pure and local (design report §38): the catalog is already in the
 * store, so opening the sheet issues no request and the first open is instant.
 */
import React, { useEffect, useMemo, useState } from 'react';
import { Drawer, Input, Skeleton } from 'antd';
import { CheckOutlined, SearchOutlined } from '@ant-design/icons';
import type { V2Application } from '@/services/runApi';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import {
  buildMobileCategoryTabs,
  buildRecentItems,
  filterMobileCatalog,
  MOBILE_SHEET_MAX_ROWS,
  splitMobileCatalog,
} from '@/lib/mobileCatalog';
import './MobileSheets.css';

export interface MobileCatalogSheetProps {
  open: boolean;
  type: 'agent' | 'app';
  applications: V2Application[];
  recentIds: number[];
  /** The application in play right now — marked with ✓ (§5.5). */
  activeApplicationId?: number | null;
  /** Show row skeletons instead of an 空列表 while the catalog loads (§15.1). */
  loading?: boolean;
  onClose: () => void;
  onSelect: (application: V2Application) => void;
}

const TITLE: Record<'agent' | 'app', string> = {
  agent: '选择智能体',
  app: '选择应用',
};

const SEARCH_PLACEHOLDER: Record<'agent' | 'app', string> = {
  agent: '搜索智能体名称或描述...',
  app: '搜索应用名称或描述...',
};

const EMPTY_TITLE: Record<'agent' | 'app', string> = {
  agent: '暂无可用智能体',
  app: '暂无可用应用',
};

function CatalogRow({
  application, active, onSelect,
}: {
  application: V2Application;
  active: boolean;
  onSelect: (application: V2Application) => void;
}) {
  return (
    <button type="button" className="mobile-sheet__row" onClick={() => onSelect(application)}>
      <AgentAvatar
        application={application}
        size={36}
        shape="circle"
        tint={application.color}
      />
      <span className="mobile-sheet__row-body">
        <span className="mobile-sheet__row-name">
          <span>{application.name}</span>
          {application.is_default_agent && (
            <span className="mobile-sheet__badge" title="工作台默认主智能体">主</span>
          )}
        </span>
        {application.description && (
          <span className="mobile-sheet__row-desc">{application.description}</span>
        )}
      </span>
      {active && <CheckOutlined className="mobile-sheet__tick" aria-label="当前" />}
    </button>
  );
}

function RowSkeletons() {
  return (
    <div className="mobile-sheet__skeleton">
      {[0, 1, 2, 3].map((i) => (
        <Skeleton key={i} active avatar paragraph={{ rows: 1, width: '55%' }} title={false} />
      ))}
    </div>
  );
}

const MobileCatalogSheet: React.FC<MobileCatalogSheetProps> = ({
  open,
  type,
  applications,
  recentIds,
  activeApplicationId,
  loading = false,
  onClose,
  onSelect,
}) => {
  const [category, setCategory] = useState('all');
  const [query, setQuery] = useState('');
  const [searchMode, setSearchMode] = useState(false);
  /** Render budget for the list below 最近使用 (see MOBILE_SHEET_MAX_ROWS). */
  const [visibleCount, setVisibleCount] = useState(MOBILE_SHEET_MAX_ROWS);

  // A reopened sheet must never be stuck on the previous search or tab — that
  // is the classic "why is my list empty" bug (§5.2).
  useEffect(() => {
    if (open) return;
    setCategory('all');
    setQuery('');
    setSearchMode(false);
    setVisibleCount(MOBILE_SHEET_MAX_ROWS);
  }, [open]);

  // A new filter is a new result set: it starts at page one, and the query is
  // cleared so the rows revealed by 加载更多 are always the SAME result the
  // user is filtering, never a slice of a stale one.
  useEffect(() => {
    setVisibleCount(MOBILE_SHEET_MAX_ROWS);
  }, [category, query, type]);

  const pool = useMemo(() => {
    const split = splitMobileCatalog(applications);
    return type === 'agent' ? split.agents : split.apps;
  }, [applications, type]);

  const recent = useMemo(
    () => buildRecentItems(pool, recentIds, type),
    [pool, recentIds, type],
  );

  const tabs = useMemo(() => buildMobileCategoryTabs(pool), [pool]);

  // Filtering happens on the COMPLETE pool; only rendering is capped. Search
  // must stay able to find an agent that is not on the first page, otherwise
  // capping the render would quietly hide part of the catalog from admins.
  const filtered = useMemo(
    () => filterMobileCatalog(pool, category, query),
    [pool, category, query],
  );

  const rendered = useMemo(
    () => filtered.slice(0, visibleCount),
    [filtered, visibleCount],
  );
  const hasMore = filtered.length > rendered.length;

  const searching = searchMode && query.trim().length > 0;
  // 最近使用 is hidden while searching so the results own the whole list (§5.4).
  const showRecent = !searching && recent.length > 0;

  const handleSelect = (application: V2Application) => {
    onSelect(application);
    onClose();
  };

  const renderBody = () => {
    if (loading && !pool.length) return <RowSkeletons />;

    if (!pool.length) {
      return (
        <div className="mobile-sheet__empty">
          <strong>{EMPTY_TITLE[type]}</strong>
          请联系管理员配置
        </div>
      );
    }

    return (
      <>
        {showRecent && (
          <>
            <div className="mobile-sheet__section-label">最近使用</div>
            {recent.map((app) => (
              <CatalogRow
                key={`recent-${app.id}`}
                application={app}
                active={app.id === activeApplicationId}
                onSelect={handleSelect}
              />
            ))}
            <div className="mobile-sheet__divider" />
          </>
        )}

        {!searching && tabs.length > 0 && (
          <div className="mobile-sheet__tabs">
            <button
              type="button"
              className={`mobile-sheet__tab${category === 'all' ? ' mobile-sheet__tab--active' : ''}`}
              onClick={() => setCategory('all')}
            >
              全部
            </button>
            {tabs.map((tab) => (
              <button
                key={tab.slug}
                type="button"
                className={`mobile-sheet__tab${category === tab.slug ? ' mobile-sheet__tab--active' : ''}`}
                onClick={() => setCategory(tab.slug)}
              >
                {tab.name}
              </button>
            ))}
          </div>
        )}

        {!searching && !showRecent && tabs.length === 0 && (
          <div className="mobile-sheet__section-label">
            {type === 'agent' ? '全部智能体' : '全部应用'}
          </div>
        )}

        {filtered.length === 0 ? (
          // An unmatched search must NOT fall back to showing everything (§15.3).
          <div className="mobile-sheet__empty">
            {searching ? <>没有找到“{query.trim()}”</> : <>该分类下暂无内容</>}
          </div>
        ) : (
          <>
            {rendered.map((app) => (
              <CatalogRow
                key={app.id}
                application={app}
                active={app.id === activeApplicationId}
                onSelect={handleSelect}
              />
            ))}
            {hasMore && (
              <button
                type="button"
                className="mobile-sheet__more-btn"
                onClick={() => setVisibleCount((current) => current + MOBILE_SHEET_MAX_ROWS)}
              >
                加载更多（还有 {filtered.length - rendered.length} 个）
              </button>
            )}
          </>
        )}
      </>
    );
  };

  return (
    <Drawer
      placement="bottom"
      open={open}
      onClose={onClose}
      height="82dvh"
      closable={false}
      title={null}
      rootClassName="mobile-bottom-sheet"
      // 22–24px top radius (§13.1). Set inline as well as in CSS so the shape
      // survives antd version differences in the content class path.
      styles={{
        content: { borderRadius: '24px 24px 0 0' },
        body: { padding: 0, display: 'flex', flexDirection: 'column', minHeight: 0 },
      }}
    >
      <div className="mobile-sheet">
        <div className="mobile-sheet__handle" aria-hidden />

        <div className="mobile-sheet__header">
          {searchMode ? (
            <>
              <Input
                className="mobile-sheet__search"
                autoFocus
                allowClear
                value={query}
                placeholder={SEARCH_PLACEHOLDER[type]}
                onChange={(e) => setQuery(e.target.value)}
              />
              <button
                type="button"
                className="mobile-sheet__cancel"
                onClick={() => { setSearchMode(false); setQuery(''); }}
              >
                取消
              </button>
            </>
          ) : (
            <>
              <span className="mobile-sheet__title">{TITLE[type]}</span>
              <button
                type="button"
                className="mobile-sheet__icon-btn"
                aria-label="搜索"
                onClick={() => setSearchMode(true)}
              >
                <SearchOutlined />
              </button>
            </>
          )}
        </div>

        <div className="mobile-sheet__body">{renderBody()}</div>
      </div>
    </Drawer>
  );
};

export default MobileCatalogSheet;
