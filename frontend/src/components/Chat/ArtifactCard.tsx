import React from 'react';
import { FileImageOutlined, FileTextOutlined, LinkOutlined } from '@ant-design/icons';
import type { ChatArtifact } from '@/stores/useRunChatStore';

/** Artifact card — click opens via the 24h-refresh 302 endpoint. */
const ArtifactCard: React.FC<{ artifact: ChatArtifact }> = ({ artifact }) => {
  const isImage = artifact.normalizedType.includes('image')
    || artifact.name.match(/\.(png|jpe?g)$/i);
  const label = artifact.name || '生成产物';

  const open = () => {
    // GET /api/v2/artifacts/{id}/open resolves a fresh provider URL and
    // 302-redirects; open in a new tab so the workspace stays mounted.
    const baseUrl = import.meta.env.VITE_API_BASE_URL || '/api';
    window.open(`${baseUrl}/v2/artifacts/${artifact.artifactId}/open`, '_blank');
  };

  return (
    <button
      type="button"
      className="artifact-card"
      onClick={open}
      title={`打开产物：${label}`}
    >
      <span className="artifact-card__icon">
        {isImage ? <FileImageOutlined /> : <FileTextOutlined />}
      </span>
      <span className="artifact-card__meta">
        <span className="artifact-card__name">{label}</span>
        <span className="artifact-card__type">{artifact.normalizedType || 'file'}</span>
      </span>
      <LinkOutlined className="artifact-card__open" />
    </button>
  );
};

export default ArtifactCard;
