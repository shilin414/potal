/**
 * MobileAccessPage — 移动端权限管理（开发执行报告 §39/§40）：
 * Resource List → 点击进 Full Screen 权限编辑器。支持 ?app=id 直开。
 *
 * 二次复审 P1-1/P2-6：
 *   · 资源列表走真实分页（hasMore/loadMore）——企业资源 >100 也能管理；
 *   · ?app=id 深链不再依赖“该资源恰好落在第一页”：items 里找不到时用
 *     fetchApplicationDetail 按 ID resolve，失败给出明确错误，绝不静默降级
 *     成普通列表；
 *   · 深链失败分类（P2-6）：403/404 = 资源不存在或无权限（终态）；其它
 *     （网络中断/500/网关超时）= 加载目标资源失败 + 可重试，不再把临时
 *     故障永久解释成权限问题；点击普通行会清掉深链错误；
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
  // ?app= id gets a fresh attempt. `deepLinkAttempt` only exists to re-run
  // the effect after an explicit retry (P2-6).
  const deepLinkTriedRef = useRef<number | null>(null);
  const [deepLinkAttempt, setDeepLinkAttempt] = useState(0);

  const fatalError = Boolean(error) && items.length === 0;
  const partialError = Boolean(error) && items.length > 0;
  // A failed loadMore keeps its cursor (retry = loadMore); a failed search /
  // first page clears it (retry = refresh).
  const retryPartial = () => (hasMore ? void loadMore() : void refresh());

  // A changed ?app= id must clear the previous id's error banner (P2-6).
  useEffect(() => {
    setDeepLinkError(null);
  }, [initial]);

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
    if (loading || deepLinkTriedRef.current === initial) return;
    deepLinkTriedRef.current = initial;
    let stale = false;
    fetchApplicationDetail(initial)
      .then((detail) => {
        if (!stale) setSelected({ id: initial, name: detail.name });
      })
      .catch((err: any) => {
        if (stale) return;
        const status = err?.response?.status;
        if (status === 403 || status === 404) {
          setDeepLinkError('资源不存在或无权限');
        } else {
          // A transient failure (network / 5xx / gateway) is retryable — never
          // permanently framed as a permission problem.
          setDeepLinkError('加载目标资源失败');
        }
      });
    return () => { stale = true; };
  }, [initial, items, loading, selected, deepLinkAttempt]);

  // Every organic row open goes through here so a lingering deep-link error
  // does not survive the user simply picking something else (P2-6).
  const openTarget = (app: { id: number; name: string }) => {
    setDeepLinkError(null);
    setSelected(app);
  };

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
          onClick={() => openTarget(app)}
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
          action={deepLinkError === '加载目标资源失败' ? (
            <Button
              size="small"
              onClick={() => {
                deepLinkTriedRef.current = null;
                setDeepLinkAttempt((n) => n + 1);
              }}
            >
              重新加载目标资源
            </Button>
          ) : undefined}
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
          {/* partial 错误只展示说明（三次复审 §47–§48）：具体操作由列表底部
              的唯一 CTA 承担 —— Alert action + 底部按钮两个重试入口会打架。 */}
          {partialError && (
            <Alert
              type="error"
              showIcon
              message="加载失败"
              description={error}
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
              {/* 唯一 CTA：partial 失败 = 重试；否则加载更多。 */}
              {(hasMore || partialError) && (
                <button
                  type="button"
                  className="mobile-console-more"
                  onClick={retryPartial}
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
