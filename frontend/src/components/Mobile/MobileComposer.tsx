/**
 * MobileComposer — the Agent-style two-layer composer (design report §8).
 *
 *   ┌──────────────────────────────┐
 *   │ 输入你的任务或问题            │   ← layer 1: auto-growing textarea
 *   │ ＋     ⚡ 技能 2         ↑  │   ← layer 2: + · skills · send
 *   └──────────────────────────────┘
 *
 * Layer 2 exists because 技能 is a first-class entry, not an attachment (§9.1):
 * burying it inside `+` would misrepresent it as input data rather than task
 * context. 附件 deliberately stays inside `+`.
 *
 * Deliberate omissions:
 *   · NO microphone button — the report forbids decorative entries with no
 *     capability behind them (§8.1); voice lands later and gets the slot then.
 *   · NO hard-coded iPhone sizes — the shell's flex layout anchors the
 *     composer, `dvh` + `env(safe-area-inset-bottom)` handle the rest (§8.4).
 *   · NO `position: fixed` — the composer stays inside the workspace height
 *     chain, which is what keeps it above the soft keyboard on iOS (§8.4).
 *
 * Keyboard convention: on mobile Enter inserts a NEWLINE and the explicit
 * send button submits. A phone soft keyboard makes Enter far too easy to hit
 * by accident, and this matches 飞书/WeChat behaviour, which is the container
 * this shell actually runs in.
 */
import React, { useEffect, useRef } from 'react';
import { Spin } from 'antd';
import {
  PaperClipOutlined,
  PlusOutlined,
  SendOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import './MobileComposer.css';

/** One attachment being uploaded or already accepted by the provider. */
export interface PendingUpload {
  key: string;
  name: string;
  state: 'uploading' | 'ready' | 'failed';
  attachmentId?: string;
}

export interface MobileComposerProps {
  value: string;
  /** True while a run is being submitted or is streaming. */
  sending: boolean;
  uploads: PendingUpload[];
  supportsAttachment: boolean;
  /** Pre-computed chip label (技能 / a skill name / 技能 N) — see agentSkills. */
  skillLabel: string;
  /** True when the current agent offers at least one skill. */
  hasSkills: boolean;

  onChange: (value: string) => void;
  onSend: () => void;
  /** Open the `+` sheet (图片/文件). */
  onOpenAttachments: () => void;
  /** Open the skill sheet. */
  onOpenSkills: () => void;
  onRemoveUpload: (key: string) => void;
}

/** §24: textarea 48–140px, then it scrolls internally. */
const TEXTAREA_MIN_HEIGHT = 48;
const TEXTAREA_MAX_HEIGHT = 140;

const MobileComposer: React.FC<MobileComposerProps> = ({
  value,
  sending,
  uploads,
  supportsAttachment,
  skillLabel,
  hasSkills,
  onChange,
  onSend,
  onOpenAttachments,
  onOpenSkills,
  onRemoveUpload,
}) => {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const uploading = uploads.some((u) => u.state === 'uploading');
  const readyCount = uploads.filter((u) => u.state === 'ready').length;

  // Auto-grow. Measure by resetting to `auto` first, otherwise scrollHeight
  // only ever grows (the current inline height is included in the box).
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = `${Math.min(
      Math.max(el.scrollHeight, TEXTAREA_MIN_HEIGHT),
      TEXTAREA_MAX_HEIGHT,
    )}px`;
  }, [value]);

  const canSend = Boolean(value.trim()) && !sending && !uploading;

  return (
    <div className="mobile-composer-wrap">
      {uploads.length > 0 && (
        <div className="mobile-composer__uploads">
          {uploads.map((upload) => (
            <span key={upload.key} className="mobile-composer__upload">
              {/* Uploading keeps a visible spinner AND keeps send disabled, so
                  the user is never told "sent" for a file still in flight
                  (§11). The chip is deliberately not removable mid-flight
                  only to avoid orphaning a provider upload. */}
              {upload.state === 'uploading'
                ? <Spin size="small" />
                : <PaperClipOutlined />}
              <span className="mobile-composer__upload-name">{upload.name}</span>
              {upload.state !== 'uploading' && (
                <button
                  type="button"
                  className="mobile-composer__upload-remove"
                  aria-label={`移除 ${upload.name}`}
                  onClick={() => onRemoveUpload(upload.key)}
                >
                  ×
                </button>
              )}
            </span>
          ))}
        </div>
      )}

      <div className="mobile-composer">
        <textarea
          ref={textareaRef}
          className="mobile-composer__textarea"
          value={value}
          rows={1}
          placeholder={sending ? '回复生成中…' : '输入你的任务或问题'}
          disabled={sending}
          onChange={(e) => onChange(e.target.value)}
        />

        <div className="mobile-composer__toolbar">
          {supportsAttachment && (
            <button
              type="button"
              className="mobile-composer__icon-btn"
              aria-label="添加图片或文件"
              disabled={sending}
              onClick={onOpenAttachments}
            >
              <PlusOutlined />
            </button>
          )}

          {/* Kept in place even with no skills, so the toolbar does not shift
              between agents (§21) — the sheet explains the absence instead. */}
          <button
            type="button"
            className={`mobile-composer__skill-btn${!hasSkills ? ' mobile-composer__skill-btn--muted' : ''}`}
            disabled={sending}
            onClick={onOpenSkills}
          >
            <ThunderboltOutlined />
            <span className="mobile-composer__skill-label">{skillLabel}</span>
          </button>

          <span className="mobile-composer__spacer" />

          <button
            type="button"
            className="mobile-composer__send"
            aria-label="发送"
            disabled={!canSend}
            onClick={onSend}
          >
            <SendOutlined />
          </button>
        </div>
      </div>

      {uploading && (
        <p className="mobile-composer__hint">附件上传中，完成后即可发送…</p>
      )}
      {!uploading && readyCount > 0 && (
        <p className="mobile-composer__hint">已选 {readyCount} 个附件</p>
      )}
    </div>
  );
};

export default MobileComposer;
