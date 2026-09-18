/**
 * MobileProvidersPage — 移动端 Provider（开发执行报告 §49，二次复审 P2-9）：
 * 一个 Provider 一张 compact 卡片，不做横向表格。
 *
 * 四态齐全：loading → Skeleton；error → 错误态 + 重试；success + [] →
 * 暂无 Provider；success + data → 卡片。请求失败不再被吞成“暂无数据”。
 */
import React, { useCallback, useEffect, useState } from 'react';
import { Button, Skeleton } from 'antd';
import { fetchAgentRuntimes } from '@/services/runApi';
import { MobileEmptyState, MobilePage, MobileSection } from '@/components/MobileConsole';
import '../EnterpriseMobile.css';

export default function MobileProvidersPage() {
  const [rows, setRows] = useState<any[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    setError(null);
    fetchAgentRuntimes()
      .then(setRows)
      .catch(() => setError('加载 Provider 失败'));
  }, []);

  useEffect(() => { load(); }, [load]);

  return (
    <MobilePage>
      <MobileSection title="运行时能力">
        {error ? (
          <MobileEmptyState
            title="加载 Provider 失败"
            action={<Button onClick={load}>重试</Button>}
          />
        ) : rows === null ? (
          <div style={{ padding: 16 }}>
            <Skeleton active paragraph={{ rows: 2 }} />
            <Skeleton active paragraph={{ rows: 2 }} />
          </div>
        ) : rows.length === 0 ? (
          <MobileEmptyState title="暂无已注册的 Provider" />
        ) : rows.map((row) => (
          <div key={row.key ?? row.provider_key} className="mobile-provider-card">
            <div className="mobile-provider-card__name">
              {row.provider_name || row.provider_key}
            </div>
            <dl className="mobile-provider-card__rows">
              <div className="mobile-provider-card__row">
                <dt>Provider Key</dt>
                <dd>{row.provider_key}</dd>
              </div>
              <div className="mobile-provider-card__row">
                <dt>Runtime</dt>
                <dd>{row.runtime_type}</dd>
              </div>
              <div className="mobile-provider-card__row">
                <dt>身份模式</dt>
                <dd>{row.identity_modes?.join(' / ') || '—'}</dd>
              </div>
              <div className="mobile-provider-card__row">
                <dt>执行模式</dt>
                <dd>{row.execution_modes?.join(' / ') || '—'}</dd>
              </div>
            </dl>
          </div>
        ))}
      </MobileSection>
    </MobilePage>
  );
}
