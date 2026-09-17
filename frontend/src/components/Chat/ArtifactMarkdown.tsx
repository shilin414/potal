/**
 * Shared artifact-aware markdown rendering (chat + public share page).
 *
 * Aily embeds generated files as `artifacts/<artifactName>/<...>/<filename>`
 * sandbox-relative refs; they mean nothing outside the chat surface until
 * rewritten to an /open resolver that 302s to a fresh provider signed URL.
 * Callers choose the resolver via `openArtifactUrl` — the in-app chat uses
 * the authenticated endpoint, the public share page the token-scoped one.
 * File bytes are always fetched by the browser straight from the provider.
 */
import React, { useCallback, useMemo, useState } from 'react';
import { Spin } from 'antd';
import ReactMarkdown from 'react-markdown';

import { resolveArtifactRef } from './artifactRef';

const API_BASE = import.meta.env.VITE_API_BASE_URL || '/api';

/**
 * Stable chat image. A provider artifact URL goes through the /open 302
 * resolver (24h URL, then CDN), which is slow on first load and must not be
 * re-fetched on every store update. Keying by src and keeping load state
 * here means a re-render only recomputes the frame, never restarts the
 * download; the placeholder avoids the browser's broken-image icon while
 * the 302 resolves.
 */
export const ChatImage: React.FC<{
  src: string;
  alt?: string;
}> = ({ src, alt }) => {
  const [loaded, setLoaded] = useState(false);
  const [failed, setFailed] = useState(false);
  if (failed) {
    return <span className="chat-img chat-img--broken" title={alt}>{alt || '图片加载失败'}</span>;
  }
  return (
    <span className="chat-img">
      {!loaded && <span className="chat-img__loading"><Spin size="small" /></span>}
      <img
        src={src}
        alt={alt || ''}
        loading="lazy"
        onLoad={() => setLoaded(true)}
        onError={() => setFailed(true)}
      />
    </span>
  );
};

export interface ArtifactRef {
  artifactId: string;
  name: string;
}

export const MarkdownWithArtifacts: React.FC<{
  content: string;
  artifacts?: ArtifactRef[];
  /** Override the resolver target (public shares use the token-scoped one). */
  openArtifactUrl?: (artifactId: string) => string;
}> = ({ content, artifacts, openArtifactUrl }) => {
  const resolveUrl = openArtifactUrl
    ?? useCallback((artifactId: string) => `${API_BASE}/v2/artifacts/${artifactId}/open`, []);

  /**
   * Matched /open URL, or null when the ref is still unresolved. The matching
   * rules live in ./artifactRef so they can be unit-tested: a nested
   * `artifacts/<name>/<sub>/<file>` ref used to resolve to nothing, which is
   * why only the first of two generated images appeared.
   */
  const resolveArtifactSrc = useCallback(
    (src: string): string | null => resolveArtifactRef(src, artifacts, resolveUrl),
    [artifacts, resolveUrl]);

  // Stable component identities: ReactMarkdown's `components` prop is a
  // render-time lookup table, and an inline object would give every <img> a
  // NEW component type on each parent render — unmounting and remounting the
  // ChatImage subtree, restarting the 302 → CDN download on every content
  // delta. Memoized on artifacts only, so streaming text updates never
  // change it.
  const components = useMemo(() => ({
    img: ({ src, alt }: { src?: string; alt?: string }) => {
      if (typeof src !== 'string') {
        return <img src={src} alt={alt || ''} loading="lazy" />;
      }
      if (/^(https?:|data:|blob:)/i.test(src)) {
        return <ChatImage src={src} alt={alt} />;
      }
      const resolved = resolveArtifactSrc(src);
      // Unknown artifact ref: placeholder, never a broken <img> that
      // flickers into place once artifact.discovered lands.
      return resolved
        ? <ChatImage src={resolved} alt={alt} />
        : <span className="chat-img chat-img--pending" title={src}>{alt || '生成产物'}</span>;
    },
    a: ({ href, children, ...rest }: any) => (
      <a
        href={typeof href === 'string'
          ? (resolveArtifactSrc(href) ?? href)
          : href}
        target="_blank"
        rel="noreferrer"
        {...rest}
      >
        {children}
      </a>
    ),
  }), [resolveArtifactSrc]);

  return (
    <ReactMarkdown components={components}>
      {content}
    </ReactMarkdown>
  );
};
