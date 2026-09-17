/**
 * MobileCatalogSheet — the mobile 智能体 / 应用 picker (design report §4–§6).
 *
 * ONE component serves both pickers and every entry point (home 双入口, the
 * chat top bar, the desktop switcher's mobile counterpart), because the report
 * is explicit that they are two *catalogue entries*, not two content panes
 * (§4.1): the difference is only which slice of the catalog shows and what the
 * title says.
 *
 * Two data modes (执行报告 §25, 2026-09-17):
 *
 *  · SERVER-PAGED (production): `applications` is left undefined and the
 *    sheet fetches `GET /v2/applications/page` itself — first page on open,
 *    next cursor page on 加载更多, search/category sent to the backend. The
 *    list never depends on the whole-catalog mirror again.
 *  · LOCAL POOL (tests / fallback): when `applications` IS passed, the sheet
 *    behaves exactly as before — a pure projection of the given rows, no
 *    requests. Unit tests pin this mode.
 *
 * What it deliberately is NOT:
 *   · not a route — picking an item returns the application to the caller;
 *   · not a management surface — Provider / Runtime / 使用次数 / 编辑 / 删除
 *     stay in 智能体市场 (§5.5);
 *   · not the desktop Dropdown — that is `ApplicationSwitcher`, untouched.
 */
import React, { useEffect, useMemo, useState } from 'react';
import { Drawer, Input, Skeleton } from 'antd';
import { CheckOutlined, SearchOutlined } from '@ant-design/icons';
import type { ApplicationSummary, V2Application } from '@/services/runApi';
import { useApplicationPage } from '@/hooks/useApplicationPage';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import {
  buildMobileCategoryTabs,
  buildRecentItems,
  filterMobileCatalog,
  mergeRecentItems,
  MOBILE_SHEET_MAX_ROWS,
  MOBILE_SHEET_PAGE_SIZE,
  splitMobileCatalog,
} from '@/lib/mobileCatalog';
import './MobileSheets.css';

export interface MobileCatalogSheetProps {
  open: boolean;
  type: 'agent' | 'app';
  /**
   * Legacy local pool. UNDEFINED in production — the sheet then pages the
   * server itself; unit tests pass a pool to stay request-free.
   */
  applications?: V2Application[];
  /** The application in play right now — marked with ✓ (§5.5). */
  activeApplicationId?: number | null;
  /**
   * Server-paged mode only: the object for `activeApplicationId`, injected
   * by the caller so the tick renders even when the active row is not on a
   * fetched page (e.g. an unbound agent the list legitimately omits).
   */
  activeApplication?: ApplicationSummary | null;
  recentIds: number[];
  /** Show row skeletons instead of an 空列表 while loading (§15.1). */
  loading?: boolean;
  onClose: () => void;
  onSelect: (application: ApplicationSummary) => void;
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
  application: ApplicationSummary;
  active: boolean;
  onSelect: (application: ApplicationSummary) => void;
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
  activeApplicationId,
  activeApplication,
  recentIds,
  loading: localLoading = false,
  onClose,
  onSelect,
}) => {
  const [category, setCategory] = useState('all');
  const [query, setQuery] = useState('');
  const [searchMode, setSearchMode] = useState(false);

  // ── Server-paged mode (production) ──
  // agent sheets exclude unbound chat apps (the old 'all' catalog slice did
  // too); app sheets include them (fixed pages always used to be visible).
  // The drawer keeps this component mounted while closed, so the fetch is
  // gated on `open` — a closed sheet issues no traffic, and reopening
  // refreshes page one, so rows changed elsewhere appear without a
  // whole-app reload.
  const paged = useApplicationPage({
    kind: type === 'agent' ? 'chat' : 'fixed',
    scope: 'manage',
    includeUnbound: type === 'app',
    category: category === 'all' ? null : category,
    query,
    limit: MOBILE_SHEET_PAGE_SIZE,
    enabled: open,
  });

  // Category rails and server-side recency come from the workspace bootstrap
  // (执行报告 §6/§11, P0-R1): they describe the CATALOG, not the pages this
  // sheet happens to have fetched. Deriving them from `paged.items` is what
  // made a category tab disappear the moment it was tapped, and made
  // 最近使用 vanish for anything off page one.
  const bootstrapRecent = useWorkspaceBootstrapStore((state) => state.recent);
  const bootstrapRecentFixed = useWorkspaceBootstrapStore((state) => state.recentFixedApps);
  const agentCategories = useWorkspaceBootstrapStore((state) => state.agentCategories);
  const appCategories = useWorkspaceBootstrapStore((state) => state.appCategories);
  const loadBootstrap = useWorkspaceBootstrapStore((state) => state.load);
  const summaryById = useWorkspaceBootstrapStore((state) => state.summaryById);
  // Local recency resolves through the entity cache: opening an application is
  // what put it there, so a just-used agent is never "unknown" here.
  const entities = useApplicationEntityStore((state) => state.byId);

  // The drawer keeps this component mounted while closed, so the bootstrap
  // fetch is gated on `open` — a closed sheet issues no traffic.
  useEffect(() => {
    if (open) void loadBootstrap();
  }, [open, loadBootstrap]);

  const serverMode = applications === undefined;

  // The active row is injected so the ✓ renders even when the active
  // application is not on a fetched page. Restrained to the unfiltered view
  // (执行报告 §4.3): inside a search or a category the injected row would be
  // an application that does not belong to the result set at all.
  const canPinActive = serverMode
    && category === 'all'
    && query.trim().length === 0;
  const serverPool = useMemo(() => {
    if (!canPinActive || !activeApplication
      || paged.items.some((app) => app.id === activeApplication.id)) {
      return paged.items;
    }
    return [activeApplication, ...paged.items];
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [paged.items, activeApplication, canPinActive]);
  const serverLoading = paged.loading || paged.loadingMore;

  // A reopened sheet must never be stuck on the previous search or tab — that
  // is the classic "why is my list empty" bug (§5.2). A reopen in paged mode
  // also refetches page one (enabled flips true), so rows changed elsewhere
  // appear too.
  useEffect(() => {
    if (open) return;
    setCategory('all');
    setQuery('');
    setSearchMode(false);
  }, [open]);

  // ── Shared derivation ──
  // Paged mode reads the server pages; local mode (tests) projects the
  // injected pool. Everything below is mode-agnostic.
  const pool = useMemo(() => {
    if (applications !== undefined) {
      const split = splitMobileCatalog(applications);
      return type === 'agent' ? split.agents : split.apps;
    }
    return serverPool;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [applications, type, serverPool]);

  const recent = useMemo<ApplicationSummary[]>(() => {
    if (!serverMode) return buildRecentItems(pool, recentIds, type);
    // Server-paged mode: the shell's own recency log, resolved against the
    // entity cache and the bootstrap rows, merged with the server's recency
    // group — so the section is independent of the current page AND of the
    // selected category (执行报告 §4.2).
    const localRecency: ApplicationSummary[] = [];
    for (const id of recentIds) {
      const hit: ApplicationSummary | undefined = entities[id] ?? summaryById(id);
      if (hit) localRecency.push(hit);
    }
    return mergeRecentItems(
      localRecency,
      type === 'agent' ? bootstrapRecent : bootstrapRecentFixed,
      type,
    );
  }, [serverMode, pool, recentIds, type, entities, summaryById,
    bootstrapRecent, bootstrapRecentFixed]);

  // Tabs: the local-pool mode derives them from the injected rows (tests pin
  // that behaviour); production reads the server's rails, which include the
  // 其他 sentinel only when uncategorized rows exist. Either way the rail does
  // NOT depend on the fetched page, so tapping a category cannot delete the
  // other tabs.
  const tabs = useMemo(() => (
    serverMode
      ? (type === 'agent' ? agentCategories : appCategories)
      : buildMobileCategoryTabs(pool)
  ), [serverMode, type, agentCategories, appCategories, pool]);

  const filtered = useMemo(
    () => (serverMode ? pool : filterMobileCatalog(pool, category, query)),
    [serverMode, pool, category, query],
  );

  // LEGACY local-pool mode only: a render budget over the injected pool (the
  // test/fallback path can still hand the sheet a whole catalog — keep the
  // "cap the render, never the filter" guarantee). Server-paged mode needs no
  // budget: the browser only ever holds fetched pages.
  const [visibleCount, setVisibleCount] = useState(MOBILE_SHEET_MAX_ROWS);
  useEffect(() => {
    setVisibleCount(MOBILE_SHEET_MAX_ROWS);
  }, [category, query, type]);
  const rendered = useMemo(
    () => (serverMode ? filtered : filtered.slice(0, visibleCount)),
    [serverMode, filtered, visibleCount],
  );
  const localHasMore = filtered.length > rendered.length;
  const hasMore = serverMode ? paged.hasMore : localHasMore;
  const loading = serverMode ? serverLoading : localLoading;

  const searching = searchMode && query.trim().length > 0;
  // 最近使用 is hidden while searching so the results own the whole list (§5.4).
  const showRecent = !searching && recent.length > 0;

  const handleSelect = (application: ApplicationSummary) => {
    onSelect(application);
    onClose();
  };

  const renderBody = () => {
    const narrowed = searching || category !== 'all';
    if (loading && !pool.length) return <RowSkeletons />;

    // "No applications at all" is a CATALOG state; a search or a category that
    // returned nothing is not (执行报告 §15.2/§15.3). Distinguishing them
    // matters most in server-paged mode, where a narrowed request legitimately
    // comes back empty: telling the user "暂无可用智能体 / 请联系管理员配置"
    // there is both wrong and a dead end, because it also used to hide the tab
    // rail they would need to get back.
    if (!pool.length && !narrowed) {
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

        {!searching && !showRecent && tabs.length === 0 && pool.length > 0 && (
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
                onClick={() => (serverMode
                  ? void paged.loadMore()
                  : setVisibleCount((current: number) => current + MOBILE_SHEET_MAX_ROWS))}
              >
                加载更多
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
