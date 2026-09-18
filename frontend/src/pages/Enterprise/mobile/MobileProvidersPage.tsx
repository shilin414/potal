/**
 * MobileProvidersPage — 移动端 Provider（开发执行报告 §49）：
 * 一个 Provider 一张 compact 卡片，不做横向表格。
 */
import React, { useEffect, useState } from 'react';
import { fetchAgentRuntimes } from '@/services/runApi';
import { MobileEmptyState, MobilePage, MobileSection } from '@/components/MobileConsole';
import '../EnterpriseMobile.css';

export default function MobileProvidersPage() {
  const [rows, setRows] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    void fetchAgentRuntimes()
      .then(setRows)
      .catch(() => setRows([]))
      .finally(() => setLoading(false));
  }, []);

  return (
    <MobilePage>
      <MobileSection title="运行时能力">
        {!loading && rows.length === 0 ? (
          <MobileEmptyState title="暂无已注册的 Provider" />
        ) : (
          rows.map((row) => (
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
          ))
        )}
      </MobileSection>
    </MobilePage>
  );
}
