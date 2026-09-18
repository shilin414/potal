/**
 * MobileCatalogContent — the ONE catalog browsing implementation shared by
 * the mobile pickers (Bottom Sheet) and the mobile centres (full pages)
 * (开发执行报告 §21/§22).
 *
 * Extracted verbatim from MobileCatalogSheet so the two surfaces can never
 * disagree; the sheet keeps only its Drawer chrome (handle + rounded panel).
 * Every behavioural rule the sheet's tests pin travels with this component:
 *
 *   · server-paged production mode + legacy local-pool test mode (§25);
 *   · 最近使用 max 3, hidden while searching, independent of page/category;
 *   · category rail from the workspace bootstrap, not the fetched page;
 *   · the active ✓ pinned only in the unfiltered view (§4.3);
 *   · disabled rows never listed (consume mode);
 *   · 加载更多 = a real cursor request in paged mode.
 *
 * mode 'page' (智能体中心/应用中心): a MobileSearchBar + category rail stick
 * above the list, rows render as rich MobileEntityRow (§12/§14/§19), and the
 * agent page enables favorites (§15) — same payload, same paged endpoint.
 */
import React, { useEffect, useMemo, useState } from 'react';
import { Button, Input, Skeleton, message } from 'antd';
import { CheckOutlined, SearchOutlined } from '@ant-design/icons';
import type { ApplicationSummary, V2Application } from '@/services/runApi';
import { setApplicationFavorite } from '@/services/runApi';
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
import {
  MobileCategoryRail,
  MobileEmptyState,
  MobileEntityRow,
  MobileSearchBar,
  MobileSection,
} from '@/components/MobileConsole';
import './MobileSheets.css';
import '@/components/MobileConsole/MobileConsole.css';

export interface MobileCatalogContentProps {
  type: 'agent' | 'app';
  /** 'sheet' = inside MobileCatalogSheet; 'page' = a centre page body. */
  mode: 'sheet' | 'page';
  /** Fetch gate — the sheet passes its `open`; a page leaves it true. */
  active?: boolean;
  /** Legacy local pool. UNDEFINED in production (server-paged mode). */
  applications?: V2Application[];
  /** The application in play right now — marked with ✓ (sheet, §5.5). */
  activeApplicationId?: number | null;
  activeApplication?: ApplicationSummary | null;
  recentIds: number[];
  /** Legacy local-pool loading flag. */
  loading?: boolean;
  onSelect: (application: ApplicationSummary) => void;
  /** Sheet mode: close after select. */
  onClose?: () => void;
  /** Page mode: render the favorite star and toggle (agent centre, §15). */
  favorites?: boolean;
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

const KIND_LABELS: Record<string, string> = {
  page: '页面',
  form: '表单',
  dashboard: '看板',
  custom: '应用',
  task: '任务',
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

/** The rich page row (§14): avatar / name / desc / 分类 · Provider / ☆ / >. */
function PageRow({
  application, favorites, onFavoriteToggle, onSelect,
}: {
  application: ApplicationSummary;
  favorites?: boolean;
  onFavoriteToggle?: (application: ApplicationSummary) => void;
  onSelect: (application: ApplicationSummary) => void;
}) {
  const isAgent = application.kind === 'chat';
  const meta = isAgent
    ? [application.category_name, application.provider_key || '企业智能体']
    : [application.category_name, KIND_LABELS[application.kind] || application.kind];
  const favorite = favorites ? (application as V2Application).is_favorite : undefined;
  return (
    <MobileEntityRow
      avatar={(
        <AgentAvatar
          application={application}
          size={44}
          shape={isAgent ? 'circle' : 'square'}
          tint={application.color}
        />
      )}
      title={application.name}
      description={application.description || undefined}
      meta={meta.filter(Boolean).join(' · ') || undefined}
      badge={application.is_default_agent ? '默认' : undefined}
      favorite={typeof favorite === 'boolean' ? favorite : undefined}
      onFavorite={typeof favorite === 'boolean' && onFavoriteToggle
        ? () => onFavoriteToggle(application)
        : undefined}
      onClick={() => onSelect(application)}
    />
  );
}

const MobileCatalogContent: React.FC<MobileCatalogContentProps> = ({
  type,
  mode,
  active = true,
  applications,
  activeApplicationId,
  activeApplication,
  recentIds,
  loading: localLoading = false,
  onSelect,
  onClose,
  favorites = false,
}) => {
  const [category, setCategory] = useState('all');
  const [query, setQuery] = useState('');
  const [searchMode, setSearchMode] = useState(false);

  // Legacy local-pool mode must never fire a server request (P2-3):
  // `serverMode` is computed BEFORE the hook so `enabled` gates ALL fetches.
  const serverMode = applications === undefined;

  // ── Server-paged mode (production) ──
  // agent lists exclude unbound chat apps; consume mode keeps 停用/未绑定
  // rows out (see MobileCatalogSheet's history — rules preserved §22).
  const paged = useApplicationPage({
    kind: type === 'agent' ? 'chat' : 'fixed',
    scope: 'accessible',
    mode: 'consume',
    category: category === 'all' ? null : category,
    query,
    limit: MOBILE_SHEET_PAGE_SIZE,
    enabled: active && serverMode,
  });

  // Category rails and server-side recency come from the workspace bootstrap:
  // they describe the CATALOG, never the pages currently fetched.
  const bootstrapRecent = useWorkspaceBootstrapStore((state) => state.recent);
  const bootstrapRecentFixed = useWorkspaceBootstrapStore((state) => state.recentFixedApps);
  const agentCategories = useWorkspaceBootstrapStore((state) => state.agentCategories);
  const appCategories = useWorkspaceBootstrapStore((state) => state.appCategories);
  const loadBootstrap = useWorkspaceBootstrapStore((state) => state.load);
  const summaryById = useWorkspaceBootstrapStore((state) => state.summaryById);
  const entities = useApplicationEntityStore((state) => state.byId);

  // Bootstrap only serves server mode (categories / server recency) — a
  // legacy local pool derives everything from the pool itself and must stay
  // fully offline (二次复审 P2-4).
  useEffect(() => {
    if (active && serverMode) void loadBootstrap();
  }, [active, serverMode, loadBootstrap]);

  // The active row is injected only in the unfiltered view (§4.3).
  const canPinActive = serverMode
    && category === 'all'
    && query.trim().length === 0;
  const serverPool = useMemo(() => {
    if (!canPinActive || !activeApplication
      || paged.items.some((app) => app.id === activeApplication.id)) {
      return paged.items;
    }
    return [activeApplication, ...paged.items];
  }, [paged.items, activeApplication, canPinActive]);
  const serverLoading = paged.loading || paged.loadingMore;

  // A reopened sheet must never be stuck on the previous search or tab.
  useEffect(() => {
    if (active) return;
    setCategory('all');
    setQuery('');
    setSearchMode(false);
  }, [active]);

  // ── Shared derivation ──
  const pool = useMemo(() => {
    if (applications !== undefined) {
      const split = splitMobileCatalog(applications);
      return type === 'agent' ? split.agents : split.apps;
    }
    return serverPool;
  }, [applications, type, serverPool]);

  const recent = useMemo<ApplicationSummary[]>(() => {
    if (!serverMode) return buildRecentItems(pool, recentIds, type);
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

  const tabs = useMemo(() => (
    serverMode
      ? (type === 'agent' ? agentCategories : appCategories)
      : buildMobileCategoryTabs(pool)
  ), [serverMode, type, agentCategories, appCategories, pool]);

  const filtered = useMemo(
    () => (serverMode ? pool : filterMobileCatalog(pool, category, query)),
    [serverMode, pool, category, query],
  );

  // LEGACY local-pool render budget (server mode pages itself).
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

  const searching = mode === 'page'
    ? query.trim().length > 0
    : searchMode && query.trim().length > 0;
  // 最近使用 is hidden while searching so the results own the whole list (§5.4).
  const showRecent = !searching && recent.length > 0;

  const handleSelect = (application: ApplicationSummary) => {
    onSelect(application);
    if (mode === 'sheet') onClose?.();
  };

  // Favorite toggle (page mode, agent centre §15): same endpoint and silent
  // optimistic patch as the desktop grid; only failures toast.
  const toggleFavorite = async (application: ApplicationSummary) => {
    const next = !(application as V2Application).is_favorite;
    try {
      await setApplicationFavorite(application.id, next);
      paged.patchItem(application.id, { is_favorite: next });
    } catch {
      message.error('收藏操作失败');
    }
  };

  const narrowed = searching || category !== 'all';
  // A request failure must never masquerade as an empty catalog (P1-3):
  // fatal = the first page failed with nothing loaded (full error state);
  // partial = a later loadMore failed (keep the loaded rows, retry inline —
  // useApplicationPage deliberately preserves already-loaded items on error).
  const fatalError = serverMode && Boolean(paged.error) && pool.length === 0;
  const partialError = serverMode && Boolean(paged.error) && pool.length > 0;
  const catalogEmpty = !pool.length && !narrowed && !fatalError;
  // A failed loadMore keeps its cursor (retry = loadMore); a failed search /
  // first page clears it (retry = refresh) — pick by what the hook still offers.
  const retryPartial = () => (paged.hasMore ? void paged.loadMore() : void paged.refresh());

  // ── Sheet body: identical markup to the pre-extraction sheet ──
  const renderSheetBody = () => {
    if (loading && !pool.length) return <RowSkeletons />;
    if (fatalError) {
      return (
        <div className="mobile-sheet__empty">
          <strong>加载失败</strong>
          请检查网络后重试
          <button
            type="button"
            className="mobile-sheet__more-btn"
            onClick={() => void paged.refresh()}
          >
            重试
          </button>
        </div>
      );
    }
    if (catalogEmpty) {
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
          searching ? (
            <div className="mobile-sheet__empty">没有找到“{query.trim()}”</div>
          ) : (
            <div className="mobile-sheet__empty">该分类下暂无内容</div>
          )
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
            {/* One CTA, never two (P2-5): a failed loadMore REPLACES 加载更多. */}
            {partialError ? (
              <button
                type="button"
                className="mobile-sheet__more-btn"
                onClick={retryPartial}
              >
                加载失败，点击重试
              </button>
            ) : hasMore ? (
              <button
                type="button"
                className="mobile-sheet__more-btn"
                onClick={() => (serverMode
                  ? void paged.loadMore()
                  : setVisibleCount((current: number) => current + MOBILE_SHEET_MAX_ROWS))}
              >
                加载更多
              </button>
            ) : null}
          </>
        )}
      </>
    );
  };

  if (mode === 'sheet') {
    // Fragment, not a wrapper: the sheet's own `.mobile-sheet` flex column
    // (Drawer chrome) owns handle + header + body nesting (§57).
    return (
      <>
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
        <div className="mobile-sheet__body">{renderSheetBody()}</div>
      </>
    );
  }

  // ── Page body: search + rail + recent + full list (§12/§19) ──
  const renderPageList = () => {
    if (loading && !pool.length) return <RowSkeletons />;
    if (fatalError) {
      return (
        <MobileEmptyState
          title="加载失败"
          hint={paged.error ?? undefined}
          action={<Button onClick={() => void paged.refresh()}>重新加载</Button>}
        />
      );
    }
    if (catalogEmpty) {
      return (
        <MobileEmptyState
          title={EMPTY_TITLE[type]}
          hint={type === 'agent' ? '请联系管理员为你分配智能体' : '请联系管理员配置'}
        />
      );
    }
    if (filtered.length === 0) {
      return (
        <MobileEmptyState
          title={searching ? `没有找到“${query.trim()}”` : '该分类下暂无内容'}
        />
      );
    }
    return (
      <>
        {rendered.map((app) => (
          <PageRow
            key={app.id}
            application={app}
            favorites={favorites}
            onFavoriteToggle={(a) => void toggleFavorite(a)}
            onSelect={handleSelect}
          />
        ))}
        {/* One CTA, never two (P2-5): a failed loadMore REPLACES 加载更多. */}
        {partialError ? (
          <button
            type="button"
            className="mobile-console-more"
            onClick={retryPartial}
          >
            加载失败，点击重试
          </button>
        ) : hasMore ? (
          <button
            type="button"
            className="mobile-console-more"
            onClick={() => (serverMode
              ? void paged.loadMore()
              : setVisibleCount((current: number) => current + MOBILE_SHEET_MAX_ROWS))}
          >
            加载更多
          </button>
        ) : null}
      </>
    );
  };

  return (
    <>
      <div className="mobile-console-page__sticky">
        <MobileSearchBar
          placeholder={type === 'agent' ? '搜索智能体' : '搜索应用'}
          value={query}
          onChange={setQuery}
        />
        {!searching && tabs.length > 0 && (
          <MobileCategoryRail
            categories={tabs}
            value={category}
            onChange={setCategory}
          />
        )}
      </div>

      {showRecent && (
        <MobileSection title="最近使用" flush>
          {recent.map((app) => (
            <PageRow
              key={`recent-${app.id}`}
              application={app}
              onSelect={handleSelect}
            />
          ))}
        </MobileSection>
      )}

      <MobileSection
        title={searching ? undefined : (type === 'agent' ? '全部智能体' : '全部应用')}
        flush
      >
        {renderPageList()}
      </MobileSection>
    </>
  );
};

export default MobileCatalogContent;
