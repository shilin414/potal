import React from 'react';
import { Empty } from 'antd';
import type { ProjectAsset } from '@/types/workflow';
import './AssetPanel.css';

interface AssetPanelProps {
  assets: ProjectAsset[];
  /** Subtitle under the title; defaults to the workspace/chat wording. */
  hint?: string;
}

const AssetPanel: React.FC<AssetPanelProps> = ({ assets, hint }) => {
  const images = assets.filter((a) => a.asset_type === 'image');
  const texts = assets.filter((a) => a.asset_type === 'text');
  const others = assets.filter((a) => !['image', 'text'].includes(a.asset_type));

  return (
    <aside className="ws-asset-panel">
      <div className="ws-asset-head">
        <div className="ws-asset-title">工作空间文件</div>
        <div className="ws-asset-sub">{hint ?? '对话中生成的内容会自动收集到这里'}</div>
      </div>

      {assets.length === 0 ? (
        <div className="ws-asset-empty">
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={<span className="text-text-dim">还没有生成的内容</span>}
          />
        </div>
      ) : (
        <div className="ws-asset-body">
          {images.length > 0 && (
            <Section title={`图片 · ${images.length}`}>
              <div className="ws-asset-grid">
                {images.map((a) => (
                  <div className="ws-asset-image" key={a.id} title={a.name}>
                    {a.url ? (
                      <img src={a.url} alt={a.name} loading="lazy" />
                    ) : (
                      <div className="ws-asset-placeholder">🖼️</div>
                    )}
                  </div>
                ))}
              </div>
            </Section>
          )}

          {texts.length > 0 && (
            <Section title={`文本 · ${texts.length}`}>
              <div className="ws-asset-texts">
                {texts.map((a) => (
                  <div className="ws-asset-text" key={a.id}>
                    <div className="ws-asset-text-name">{a.name}</div>
                    <div className="ws-asset-text-content">
                      {(a.content || '').slice(0, 140)}
                      {(a.content || '').length > 140 ? '…' : ''}
                    </div>
                  </div>
                ))}
              </div>
            </Section>
          )}

          {others.length > 0 && (
            <Section title={`其他 · ${others.length}`}>
              <div className="ws-asset-texts">
                {others.map((a) => (
                  <div className="ws-asset-text" key={a.id}>
                    <div className="ws-asset-text-name">{a.name}</div>
                    <div className="ws-asset-text-content">{a.url || a.content || '—'}</div>
                  </div>
                ))}
              </div>
            </Section>
          )}
        </div>
      )}
    </aside>
  );
};

const Section: React.FC<{ title: string; children: React.ReactNode }> = ({ title, children }) => (
  <div className="ws-asset-section">
    <div className="ws-asset-section-title">{title}</div>
    {children}
  </div>
);

export default AssetPanel;
