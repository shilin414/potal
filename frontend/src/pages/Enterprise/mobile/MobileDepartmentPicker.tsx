/**
 * MobileDepartmentPicker — 移动端部门选择 Bottom Sheet（开发执行报告 §42）。
 *
 * 第一版采用 Flat Search（报告推荐）：不做树形展开，直接在已加载的
 * 部门平表里按名称过滤，行上展示「父级 / 部门」路径。勾选即多选，
 * 底部固定「完成」。数据由调用方（权限编辑器）传入，不重复请求。
 */
import React, { useMemo, useState } from 'react';
import { Drawer, Input } from 'antd';
import { CheckOutlined, SearchOutlined } from '@ant-design/icons';
import type { DirectoryDepartment } from '../enterpriseApi';
import '../EnterpriseMobile.css';

export interface MobileDepartmentPickerProps {
  open: boolean;
  departments: DirectoryDepartment[];
  selectedIds: number[];
  onClose: () => void;
  onDone: (ids: number[]) => void;
}

/** "财务中心 / 财务一部" — the flat list needs ancestry to disambiguate. */
function pathOf(dep: DirectoryDepartment, byId: Map<number, DirectoryDepartment>): string {
  const parts: string[] = [];
  let node: DirectoryDepartment | undefined = dep;
  let guard = 0;
  while (node && guard++ < 10) {
    parts.unshift(node.name);
    node = node.parent_id ? byId.get(node.parent_id) : undefined;
  }
  return parts.join(' / ');
}

export default function MobileDepartmentPicker({
  open, departments, selectedIds, onClose, onDone,
}: MobileDepartmentPickerProps) {
  const [query, setQuery] = useState('');
  const [picked, setPicked] = useState<number[]>(selectedIds);

  // Re-seed the local selection each time the sheet opens.
  React.useEffect(() => {
    if (open) setPicked(selectedIds);
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  const byId = useMemo(
    () => new Map(departments.map((d) => [d.id, d])), [departments]);
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return departments;
    return departments.filter((d) => d.name.toLowerCase().includes(q));
  }, [departments, query]);

  const toggle = (id: number) => {
    setPicked((current) => (
      current.includes(id) ? current.filter((x) => x !== id) : [...current, id]));
  };

  return (
    <Drawer
      placement="bottom"
      open={open}
      onClose={onClose}
      height="72dvh"
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
          <span className="mobile-sheet__title">选择部门</span>
        </div>
        <div className="mobile-picker">
          <div className="mobile-picker__search">
            <Input
              allowClear
              value={query}
              placeholder="搜索部门名称"
              prefix={<SearchOutlined style={{ color: 'var(--color-text-dim)' }} />}
              onChange={(e) => setQuery(e.target.value)}
            />
          </div>
          <div className="mobile-picker__body">
            {filtered.length === 0 ? (
              <div className="mobile-console-empty">没有匹配的部门</div>
            ) : filtered.map((dep) => {
              const checked = picked.includes(dep.id);
              return (
                <button
                  key={dep.id}
                  type="button"
                  className="mobile-picker__row"
                  aria-pressed={checked}
                  onClick={() => toggle(dep.id)}
                >
                  <span className="mobile-picker__row-body">
                    <span className="mobile-picker__row-name">{dep.name}</span>
                    <span className="mobile-picker__row-meta">{pathOf(dep, byId)}</span>
                  </span>
                  <span className={`mobile-picker__check${checked ? ' mobile-picker__check--on' : ''}`}>
                    <CheckOutlined />
                  </span>
                </button>
              );
            })}
          </div>
          <div className="mobile-picker__footer">
            <span className="mobile-picker__footer-count">已选择 {picked.length} 个</span>
            <button
              type="button"
              className="mobile-picker__done"
              onClick={() => onDone(picked)}
            >
              完成
            </button>
          </div>
        </div>
      </div>
    </Drawer>
  );
}
