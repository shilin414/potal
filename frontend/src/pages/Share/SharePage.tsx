/**
 * SharePage — public read-only view of a conversation share snapshot.
 *
 * The token identifies a server-side snapshot: the backend returns ONLY the
 * messages pinned at share creation time (never the rest of the conversation),
 * so nothing here depends on client-side filtering. No authentication and no
 * shell — receivers open the link directly.
 */
import React, { useEffect, useMemo, useState } from 'react';
import { useParams } from 'react-router-dom';
import { Result, Spin } from 'antd';
import Avatar from 'antd/es/avatar';
import { MarkdownWithArtifacts } from '@/components/Chat/ArtifactMarkdown';
import '@/components/Chat/RunChatPanel.css';
import {
  getPublicShare,
  type PublicShareDetail,
} from '@/services/shareApi';
import './SharePage.css';

const formatTime = (iso: string) => {
  const t = new Date(iso);
  return Number.isNaN(t.getTime()) ? '' : t.toLocaleString();
};

const SharePage: React.FC = () => {
  const { token } = useParams<{ token: string }>();
  const [data, setData] = useState<PublicShareDetail | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    if (!token) {
      setNotFound(true);
      setLoading(false);
      return;
    }
    setLoading(true);
    getPublicShare(token)
      .then((detail) => {
        if (!cancelled) setData(detail);
      })
      .catch(() => {
        if (!cancelled) setNotFound(true);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  if (loading) {
    return (
      <div className="share-page share-page--center">
        <Spin size="large" />
        <p className="share-page__loading-hint">正在加载分享内容…</p>
      </div>
    );
  }

  if (notFound || !data) {
    return (
      <div className="share-page share-page--center">
        <Result
          status="404"
          title="分享不存在或已撤销"
          subTitle="链接可能已失效，请联系分享者重新获取。"
        />
      </div>
    );
  }

  return (
    <div className="share-page">
      <header className="share-header">
        <div className="share-header__brand">✦ Creation Agent Studio</div>
        <h1 className="share-header__title" title={data.title}>
          {data.title || '分享的对话'}
        </h1>
        <div className="share-header__meta">
          <span className="share-header__tag">只读快照</span>
          <span>{data.messages.length} 条消息</span>
          <span>分享于 {formatTime(data.shared_at)}</span>
        </div>
      </header>

      <main className="share-messages">
        <div className="share-messages__inner">
          {data.messages.map((msg, i) => {
            const isUser = msg.role === 'user';
            const isSystem = msg.role === 'system';
            const sender = isUser ? '用户' : isSystem ? '系统' : '智能体';
            return (
              <div
                key={i}
                className={`share-msg ${isUser ? 'share-msg--user' : ''} animate-fade-in`}
              >
                <Avatar
                  size={36}
                  className="share-msg__avatar"
                  style={{
                    backgroundColor: isUser
                      ? 'var(--color-primary)'
                      : 'var(--color-bg-elevated)',
                    color: isUser ? '#fff' : 'var(--color-primary)',
                  }}
                >
                  {isUser ? '用' : isSystem ? '系' : 'AI'}
                </Avatar>
                <div className={`share-msg__bubble ${isUser ? 'share-msg__bubble--user' : ''}`}>
                  <div className="share-msg__header">
                    <span className="share-msg__sender">{sender}</span>
                    <span className="share-msg__time">{formatTime(msg.created_at)}</span>
                  </div>
                  {isUser || isSystem ? (
                    <span className="share-msg__plain">{msg.content}</span>
                  ) : (
                    <div className="prose prose-sm dark:prose-invert max-w-none">
                      <MarkdownWithArtifacts
                        content={msg.content}
                        artifacts={(msg.artifacts || []).map((a) => ({
                          artifactId: a.artifact_id,
                          name: a.name,
                        }))}
                        openArtifactUrl={(artifactId) =>
                          `/api/v2/public/shares/${token}/artifacts/${artifactId}/open`}
                      />
                    </div>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      </main>

      <footer className="share-footer">
        此页面为只读快照，仅包含分享时选中的消息 · 由 Creation Agent Studio 生成
      </footer>
    </div>
  );
};

export default SharePage;
