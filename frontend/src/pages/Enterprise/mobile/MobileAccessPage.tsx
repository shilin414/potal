/**
 * MobileAccessPage — 移动端权限管理（开发执行报告 §39/§40）：
 * Resource List → 点击进 Full Screen 权限编辑器。支持 ?app=id 直开。
 *
 * 二次复审 P1-1：
 *   · 资源列表走真实分页（hasMore/loadMore）——企业资源 >100 也能管理；
 *   · ?app=id 深链不再依赖“该资源恰好落在第一页”：items 里找不到时用
 *     fetchApplicationDetail 按 ID resolve，失败给出明确错误，绝不静默降级
 *     成普通列表；
 *   · 请求失败 ≠ 暂无资源（P2-11）：fatal 给整页错误态，partial 保留已
 *     加载行、加载更多入口变重试。
 */
import React, { useEffect, useRef, useState } from 'react';
import { Alert, Button, Skeleton } from 'antd';
import { useLocation } from 'react-router-dom';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import { useApplicationPage } from '@/hooks/useApplicationPage';
import { fetchApplicationDetail } from '@/services/runApi';
import {
  MobileEmptyState,
  MobileEntityRow,
  MobilePage,
  MobileSearchBar,
} from '@/components/MobileConsole';
import MobilePermissionEditor from './MobilePermissionEditor';
import '../EnterpriseMobile.css';

export default function MobileAccessPage({ kind }: { kind: 'chat' | 'fixed' }) {
  const location = useLocation();
  const initial = Number(new URLSearchParams(location.search).get('app') || 0);
  const [q, setQ] = useState('');
  const {
    items, loading, loadingMore, hasMore, loadMore, error, refresh,
  } = useApplicationPage({
    kind,
    scope: 'manage',
    mode: 'manage',
    includeUnbound: true,
    query: q,
    limit: 50,
  });
  const [selected, setSelected] = useState<{ id: number; name: string } | null>(null);
  const [deepLinkError, setDeepLinkError] = useState<string | null>(null);
  // One resolve attempt per deep-link id — a LATER loadMore may still surface
  // the row organically (the items.find branch below picks it up), and a NEW
  // ?app= id gets a fresh resolve attempt.
  const deepLinkResolvedRef = useRef<number | null>(null);

  const fatalError = Boolean(error) && items.length === 0;
  const partialError = Boolean(error) && items.length > 0;
  // A failed loadMore keeps its cursor (retry = loadMore); a failed search /
  // first page clears it (retry = refresh).
  const retryPartial = () => (hasMore ? void loadMore() : void refresh());

  // 与桌面一致：?app=id 打开时自动进入该资源的权限编辑器。当前分页里
  // 找不到（>50 条的资源不在第一页）时退回 detail 接口精确 resolve。
  useEffect(() => {
    if (!initial || selected) return;
    const hit = items.find((x) => x.id === initial);
    if (hit) {
      setSelected({ id: hit.id, name: hit.name });
      setDeepLinkError(null);
      return;
    }
    if (loading || deepLinkResolvedRef.current === initial) return;
    deepLinkResolvedRef.current = initial;
    let stale = false;
    fetchApplicationDetail(initial)
      .then((detail) => {
        if (!stale) setSelected({ id: initial, name: detail.name });
      })
      .catch(() => {
        if (!stale) setDeepLinkError('资源不存在或无权限');
      });
    return () => { stale = true; };
  }, [initial, items, loading, selected]);

  const rows = (
    <div className="mobile-console-section__rows" style={{ marginTop: 12 }}>
      {items.map((app) => (
        <MobileEntityRow
          key={app.id}
          avatar={(
            <AgentAvatar
              application={app}
              size={44}
              shape={kind === 'chat' ? 'circle' : 'square'}
              tint={app.color}
            />
          )}
          title={app.name}
          description={app.slug}
          onClick={() => setSelected({ id: app.id, name: app.name })}
        />
      ))}
    </div>
  );

  return (
    <MobilePage>
      <div className="mobile-console-page__sticky">
        <MobileSearchBar placeholder="搜索资源" value={q} onChange={setQ} />
      </div>

      {deepLinkError && (
        <Alert
          type="warning"
          showIcon
          message={deepLinkError}
          style={{ marginTop: 12 }}
        />
      )}

      {fatalError ? (
        <MobileEmptyState
          title="加载资源失败"
          hint={error ?? undefined}
          action={<Button onClick={() => void refresh()}>重试</Button>}
        />
      ) : (
        <>
          {partialError && (
            <Alert
              type="error"
              showIcon
              message="加载失败"
              description={error}
              action={<Button size="small" onClick={retryPartial}>重试</Button>}
              style={{ marginTop: 12 }}
            />
          )}
          {loading && items.length === 0 ? (
            <div style={{ padding: '12px 0' }}>
              <Skeleton active avatar paragraph={{ rows: 1 }} />
              <Skeleton active avatar paragraph={{ rows: 1 }} />
            </div>
          ) : items.length === 0 ? (
            <MobileEmptyState title="暂无可配置的资源" />
          ) : (
            <>
              {rows}
              {hasMore && (
                <button
                  type="button"
                  className="mobile-console-more"
                  onClick={() => void loadMore()}
                >
                  {loadingMore ? '加载中…' : partialError ? '加载失败，点击重试' : '加载更多'}
                </button>
              )}
            </>
          )}
        </>
      )}

      <MobilePermissionEditor
        open={selected !== null}
        application={selected}
        onClose={() => setSelected(null)}
      />
    </MobilePage>
  );
}
