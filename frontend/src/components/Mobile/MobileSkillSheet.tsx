/**
 * MobileSkillSheet — multi-select 技能配置 picker (design report §9.3).
 *
 * Skills are agent-scoped, so the sheet renders exactly what the CURRENT
 * application configured — never a global skill list. An agent with none shows
 * the honest empty state (§21) rather than a fabricated capability.
 *
 * Selection is local while the sheet is open and committed on 完成, so a
 * half-finished change never leaks into the composer.
 */
import React, { useEffect, useMemo, useState } from 'react';
import { Drawer, Input } from 'antd';
import { CheckOutlined, SearchOutlined } from '@ant-design/icons';
import type { AgentSkill } from '@/services/runApi';
import { orderSkillsForSheet } from '@/lib/agentSkills';
import './MobileSheets.css';
import './MobileSkillSheet.css';

export interface MobileSkillSheetProps {
  open: boolean;
  skills: AgentSkill[];
  selectedIds: string[];
  onChange: (skillIds: string[]) => void;
  onClose: () => void;
}

const MobileSkillSheet: React.FC<MobileSkillSheetProps> = ({
  open,
  skills,
  selectedIds,
  onChange,
  onClose,
}) => {
  // Draft copy: the composer only sees the result on 完成.
  const [draft, setDraft] = useState<string[]>(selectedIds);
  const [query, setQuery] = useState('');
  const [searchMode, setSearchMode] = useState(false);

  useEffect(() => {
    if (open) setDraft(selectedIds);
    else {
      setQuery('');
      setSearchMode(false);
    }
  }, [open, selectedIds]);

  const toggle = (skillId: string) => {
    setDraft((current) => (current.includes(skillId)
      ? current.filter((id) => id !== skillId)
      : [...current, skillId]));
  };

  const ordered = useMemo(() => orderSkillsForSheet(skills, draft), [skills, draft]);

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return ordered;
    return ordered.filter((skill) => [skill.name, skill.description]
      .some((value) => (value || '').toLowerCase().includes(q)));
  }, [ordered, query]);

  const commit = () => {
    // Re-project through the catalog so the stored selection is always in
    // catalog order — the invariant the idempotency-safe content relies on.
    const wanted = new Set(draft);
    onChange(skills.filter((skill) => wanted.has(skill.id)).map((skill) => skill.id));
    onClose();
  };

  const noSkillsAtAll = skills.length === 0;

  return (
    <Drawer
      placement="bottom"
      open={open}
      onClose={onClose}
      height="70dvh"
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
          {searchMode ? (
            <>
              <Input
                className="mobile-sheet__search"
                autoFocus
                allowClear
                value={query}
                placeholder="搜索技能..."
                onChange={(e) => setQuery(e.target.value)}
              />
              <button
                type="button"
                className="mobile-sheet__cancel"
                onClick={() => { setSearchMode(false); setQuery(''); }}
              >
                取消
              </button>
            </>
          ) : (
            <>
              <span className="mobile-sheet__title">选择技能</span>
              {!noSkillsAtAll && (
                <button
                  type="button"
                  className="mobile-sheet__icon-btn"
                  aria-label="搜索技能"
                  onClick={() => setSearchMode(true)}
                >
                  <SearchOutlined />
                </button>
              )}
            </>
          )}
        </div>

        <div className="mobile-sheet__body">
          {noSkillsAtAll ? (
            // Not a failure state: the agent simply has no 技能配置 yet. Say so
            // plainly instead of offering an empty picker (§21).
            <div className="mobile-sheet__empty">
              <strong>当前智能体暂无可选技能</strong>
              技能由管理员在智能体配置中维护
            </div>
          ) : visible.length === 0 ? (
            <div className="mobile-sheet__empty">没有找到“{query.trim()}”</div>
          ) : (
            visible.map((skill) => {
              const checked = draft.includes(skill.id);
              return (
                <button
                  key={skill.id}
                  type="button"
                  className={`mobile-sheet__row mobile-skill-row${checked ? ' mobile-skill-row--on' : ''}`}
                  aria-pressed={checked}
                  onClick={() => toggle(skill.id)}
                >
                  <span className={`mobile-skill-row__box${checked ? ' mobile-skill-row__box--on' : ''}`} aria-hidden>
                    {checked && <CheckOutlined />}
                  </span>
                  <span className="mobile-sheet__row-body">
                    {/* 每项最多两行 (§9.3): the name and the one-line
                        description — never more, so rows stay scannable. */}
                    <span className="mobile-skill-row__name">{skill.name}</span>
                    {skill.description && (
                      <span className="mobile-skill-row__desc">{skill.description}</span>
                    )}
                  </span>
                </button>
              );
            })
          )}
        </div>

        {!noSkillsAtAll && (
          <div className="mobile-skill-sheet__footer">
            <button type="button" className="mobile-skill-sheet__done" onClick={commit}>
              完成{draft.length > 0 ? `（已选 ${draft.length}）` : ''}
            </button>
          </div>
        )}
      </div>
    </Drawer>
  );
};

export default MobileSkillSheet;
