/**
 * MobileAuditPage — 移动端审计日志（开发执行报告 §50）：
 * Timeline 列表；JSON detail 不整块铺出，点「查看详情」用 Bottom Sheet 看。
 * 请求失败 ≠ 暂无审计日志（二次复审 P2-9）：错误态 + 重试。
 */
import React, { useCallback, useEffect, useState } from 'react';
import { Button, Drawer, Skeleton } from 'antd';
import { enterpriseApi, type AuditLog } from '../enterpriseApi';
import { fmt } from '../enterpriseNav';
import { MobileEmptyState, MobilePage, MobileSection } from '@/components/MobileConsole';
import '../EnterpriseMobile.css';

export default function MobileAuditPage() {
  const [rows, setRows] = useState<AuditLog[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [detail, setDetail] = useState<AuditLog | null>(null);

  const load = useCallback(() => {
    setError(null);
    enterpriseApi.audits()
      .then(setRows)
      .catch(() => setError('加载审计日志失败'));
  }, []);

  useEffect(() => { load(); }, [load]);

  return (
    <MobilePage>
      <MobileSection title="关键操作" flush>
        {error ? (
          <MobileEmptyState
            title="加载审计日志失败"
            action={<Button onClick={load}>重试</Button>}
          />
        ) : rows === null ? (
          <div style={{ padding: 16 }}>
            <Skeleton active paragraph={{ rows: 2 }} />
            <Skeleton active paragraph={{ rows: 2 }} />
          </div>
        ) : rows.length === 0 ? (
          <MobileEmptyState title="暂无审计日志" />
        ) : rows.map((row) => (
          <button
            key={row.id}
            type="button"
            className="mobile-audit__item"
            style={{
              display: 'block', width: '100%', border: 'none',
              background: 'transparent', textAlign: 'left', cursor: 'pointer',
            }}
            onClick={() => setDetail(row)}
          >
            <div className="mobile-audit__item-head">
              <span className="mobile-audit__item-action">{row.action}</span>
              <time className="mobile-audit__item-time" dateTime={row.created_at}>
                {fmt(row.created_at)}
              </time>
            </div>
            <div className="mobile-audit__item-meta">
              <span>资源：{row.resource_type}:{row.resource_id}</span>
              <span>操作者：{row.user_id || '系统'}</span>
            </div>
          </button>
        ))}
      </MobileSection>

      <Drawer
        placement="bottom"
        open={detail !== null}
        onClose={() => setDetail(null)}
        height="60dvh"
        closable={false}
        title={null}
        rootClassName="mobile-bottom-sheet"
        styles={{
          content: { borderRadius: '24px 24px 0 0' },
          body: { padding: 0, display: 'flex', flexDirection: 'column', minHeight: 0 },
        }}
      >
        <div className="mobile-sheet">
          <div className="mobile-sheet__handle" aria-hidden />
          <div className="mobile-sheet__header">
            <span className="mobile-sheet__title">
              {detail ? `${detail.action} · ${detail.resource_type}:${detail.resource_id}` : ''}
            </span>
          </div>
          <div
            className="mobile-audit__detail"
            style={{ overflowY: 'auto', flex: 1, minHeight: 0 }}
          >
            {detail ? JSON.stringify(detail.detail, null, 2) : ''}
          </div>
        </div>
      </Drawer>
    </MobilePage>
  );
}
