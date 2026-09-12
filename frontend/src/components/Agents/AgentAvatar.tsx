/**
 * AgentAvatar — an Application's face, in one place.
 *
 * Every surface that lists or names an agent shows the same thing: the uploaded
 * avatar image when there is one, else the emoji icon (home shortcut cards,
 * the switcher, the chat empty state, message bubbles). Sizing/shape are the
 * caller's CSS; identity resolution stays in `lib/chatIdentity`.
 */
import React from 'react';
import {
  agentAvatarFallback,
  agentAvatarUrl,
  agentDisplayName,
} from '@/lib/chatIdentity';
import type { ChatAgentIdentity } from '@/lib/chatIdentity';
import './AgentAvatar.css';

interface Props {
  application?: ChatAgentIdentity | null;
  /** Rendered size in px (also drives the emoji font size). */
  size?: number;
  /** 'square' matches the 智能体市场 cards, 'circle' reads as a face. */
  shape?: 'square' | 'circle';
  className?: string;
  /** Optional brand colour tinted behind the emoji fallback. */
  tint?: string;
}

const AgentAvatar: React.FC<Props> = ({
  application, size = 34, shape = 'square', className, tint,
}) => {
  const url = agentAvatarUrl(application);
  const name = agentDisplayName(application);
  const style: React.CSSProperties = {
    width: size,
    height: size,
    fontSize: Math.round(size * 0.5),
  };
  // The tint only matters for the emoji fallback; an image covers it anyway.
  if (tint && !url) {
    style.background = `color-mix(in srgb, ${tint} 16%, transparent)`;
  }
  return (
    <span
      className={`agent-avatar agent-avatar--${shape} ${className || ''}`.trim()}
      style={style}
      title={name}
      data-agent-avatar={url ? 'image' : 'emoji'}
    >
      {url ? (
        <img className="agent-avatar__img" src={url} alt={`${name} 头像`} loading="lazy" />
      ) : (
        <span className="agent-avatar__emoji">{agentAvatarFallback(application)}</span>
      )}
    </span>
  );
};

export default AgentAvatar;
