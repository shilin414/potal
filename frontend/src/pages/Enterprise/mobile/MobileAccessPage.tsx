/**
 * MobileAccessPage — 移动端权限管理（开发执行报告 §39/§40）：
 * Resource List → 点击进 Full Screen 权限编辑器。支持 ?app=id 直开。
 */
import React, { useEffect, useState } from 'react';
import { Skeleton } from 'antd';
import { useLocation } from 'react-router-dom';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import { useApplicationPage } from '@/hooks/useApplicationPage';
import type { V2Application } from '@/services/runApi';
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
  const { items, loading } = useApplicationPage({
    kind,
    scope: 'manage',
    mode: 'manage',
    includeUnbound: true,
    query: q,
    limit: 100,
  });
  const [selected, setSelected] = useState<V2Application | null>(null);

  // 与桌面一致：?app=id 打开时自动进入该资源的权限编辑器。
  useEffect(() => {
    if (initial && items.length && !selected) {
      const app = items.find((x) => x.id === initial);
      if (app) setSelected(app);
    }
  }, [initial, items, selected]);

  return (
    <MobilePage>
      <div className="mobile-console-page__sticky">
        <MobileSearchBar placeholder="搜索资源" value={q} onChange={setQ} />
      </div>

      {loading && items.length === 0 ? (
        <div style={{ padding: '12px 0' }}>
          <Skeleton active avatar paragraph={{ rows: 1 }} />
          <Skeleton active avatar paragraph={{ rows: 1 }} />
        </div>
      ) : items.length === 0 ? (
        <MobileEmptyState title="暂无可配置的资源" />
      ) : (
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
              onClick={() => setSelected(app)}
            />
          ))}
        </div>
      )}

      <MobilePermissionEditor
        open={selected !== null}
        application={selected ? { id: selected.id, name: selected.name } : null}
        onClose={() => setSelected(null)}
      />
    </MobilePage>
  );
}
