/**
 * MobilePermissionEditor — 移动端访问权限编辑（开发执行报告 §41–§43）。
 *
 * Full Screen Drawer：访问范围 → 授权部门（Flat Search Picker）→ 授权人员
 * （多选 Picker）。数据模型与桌面完全一致（access_mode / departments /
 * users / include_children），保存走同一个 updateAccess 契约。
 *
 * 加载/保存状态机完全由 useAccessPolicyEditor 承载（三次复审 P0）：目标
 * 切换竞态、保存 target guard、policy.application_id 不变量都在共享层，
 * 移动端只负责呈现。
 */
import React, { useState } from 'react';
import { Button, Empty, Radio, Skeleton, Switch, message } from 'antd';
import { CloseOutlined } from '@ant-design/icons';
import type { AccessMode } from '../enterpriseApi';
import { useAccessPolicyEditor } from '../hooks/useAccessPolicyEditor';
import {
  MobileEmptyState,
  MobileFullScreenDrawer,
  MobileSection,
} from '@/components/MobileConsole';
import MobileDepartmentPicker from './MobileDepartmentPicker';
import MobileUserPicker, { type MobilePickedUser } from './MobileUserPicker';
import '../EnterpriseMobile.css';

export interface MobilePermissionEditorProps {
  open: boolean;
  application: { id: number; name: string } | null;
  onClose: () => void;
}

export default function MobilePermissionEditor({
  open, application, onClose,
}: MobilePermissionEditorProps) {
  const [depPickerOpen, setDepPickerOpen] = useState(false);
  const [userPickerOpen, setUserPickerOpen] = useState(false);

  // 名称只用于标题显示；数据面只依赖 applicationId（二次复审 P2-5），目标
  // 切换的竞态防护与保存校验全部在共享 hook（三次复审 P0）。
  const applicationId = application?.id ?? null;
  const {
    policy, departments: deps, loading, loadError, saving, ready,
    reload, save, setAccessMode, setDepartmentIds, patchDepartmentGrant,
    setUserGrants,
  } = useAccessPolicyEditor({
    applicationId,
    enabled: open,
    onSaved: () => {
      message.success('访问权限已保存');
      onClose();
    },
  });

  const setUsers = (users: MobilePickedUser[]) => {
    setUserGrants(users.map((u) => ({
      directory_user_id: u.id,
      name: u.name,
      avatar_url: u.avatar_url,
      departments: u.departments,
    })));
  };

  return (
    <>
      <MobileFullScreenDrawer
        open={open}
        title={`${application?.name ?? ''} 访问权限`}
        actionText="保存"
        actionLoading={saving}
        // ready 只在「当前目标的 policy 已加载、无错误、id 匹配」时为真 ——
        // 保存绝不带着 A 的授权写向 B（P0）。
        actionDisabled={!ready}
        onAction={() => void save()}
        onClose={onClose}
      >
        {loading ? (
          <Skeleton active />
        ) : loadError ? (
          // A failed load is recoverable in place (P2-6) — no eternal Skeleton,
          // and 保存 stays disabled until a policy is actually loaded.
          <MobileEmptyState
            title="加载访问权限失败"
            action={<Button onClick={() => void reload()}>重试</Button>}
          />
        ) : !policy ? (
          <Skeleton active />
        ) : (
          <>
            <MobileSection title="访问范围">
              <Radio.Group
                className="mobile-permission__mode"
                value={policy.access_mode}
                onChange={(e) => setAccessMode(e.target.value as AccessMode)}
              >
                <Radio value="all">全体有效员工</Radio>
                <Radio value="assigned">指定范围</Radio>
                <Radio value="admin_only">仅管理员</Radio>
              </Radio.Group>
            </MobileSection>

            {policy.access_mode === 'assigned' && (
              <>
                <MobileSection title="授权部门">
                  {policy.departments.length === 0 ? (
                    <Empty
                      image={Empty.PRESENTED_IMAGE_SIMPLE}
                      description="未授权任何部门"
                      style={{ margin: '8px 0' }}
                    />
                  ) : (
                    <div className="mobile-console-section__rows">
                      {policy.departments.map((grant) => (
                        <div key={grant.department_id} className="mobile-permission__grant">
                          <div className="mobile-permission__grant-body">
                            <span className="mobile-permission__grant-name">
                              {grant.name
                                || deps.find((d) => d.id === grant.department_id)?.name}
                            </span>
                            <span className="mobile-permission__grant-meta">
                              覆盖 {grant.covered_users || 0} 人
                            </span>
                          </div>
                          <Switch
                            size="small"
                            checked={grant.include_children}
                            checkedChildren="含子部门"
                            unCheckedChildren="仅本部门"
                            onChange={(checked) => patchDepartmentGrant(
                              grant.department_id, checked)}
                          />
                          <button
                            type="button"
                            className="mobile-permission__grant-remove"
                            aria-label={`移除部门 ${grant.name}`}
                            onClick={() => setDepartmentIds(
                              policy.departments
                                .map((d) => d.department_id)
                                .filter((id) => id !== grant.department_id))}
                          >
                            <CloseOutlined />
                          </button>
                        </div>
                      ))}
                    </div>
                  )}
                  <button
                    type="button"
                    className="mobile-permission__add"
                    onClick={() => setDepPickerOpen(true)}
                  >
                    + 添加部门
                  </button>
                </MobileSection>

                <MobileSection title="授权人员">
                  {policy.users.length === 0 ? (
                    <Empty
                      image={Empty.PRESENTED_IMAGE_SIMPLE}
                      description="未授权任何人员"
                      style={{ margin: '8px 0' }}
                    />
                  ) : (
                    <div className="mobile-console-section__rows">
                      {policy.users.map((user) => (
                        <div key={user.directory_user_id} className="mobile-permission__grant">
                          <div className="mobile-permission__grant-body">
                            <span className="mobile-permission__grant-name">{user.name}</span>
                            {user.departments?.length > 0 && (
                              <span className="mobile-permission__grant-meta">
                                {user.departments.join(' / ')}
                              </span>
                            )}
                          </div>
                          <button
                            type="button"
                            className="mobile-permission__grant-remove"
                            aria-label={`移除人员 ${user.name}`}
                            onClick={() => setUsers(
                              policy.users
                                .filter((u) => u.directory_user_id !== user.directory_user_id)
                                .map((u) => ({
                                  id: u.directory_user_id,
                                  name: u.name,
                                  avatar_url: u.avatar_url,
                                  departments: u.departments ?? [],
                                })))}
                          >
                            <CloseOutlined />
                          </button>
                        </div>
                      ))}
                    </div>
                  )}
                  <button
                    type="button"
                    className="mobile-permission__add"
                    onClick={() => setUserPickerOpen(true)}
                  >
                    + 添加人员
                  </button>
                </MobileSection>
              </>
            )}
          </>
        )}
      </MobileFullScreenDrawer>

      <MobileDepartmentPicker
        open={depPickerOpen}
        departments={deps}
        selectedIds={policy?.departments.map((d) => d.department_id) ?? []}
        onClose={() => setDepPickerOpen(false)}
        onDone={(ids) => {
          setDepartmentIds(ids);
          setDepPickerOpen(false);
        }}
      />
      <MobileUserPicker
        open={userPickerOpen}
        selectedUsers={(policy?.users ?? []).map((u) => ({
          id: u.directory_user_id,
          name: u.name,
          avatar_url: u.avatar_url,
          departments: u.departments ?? [],
        }))}
        onClose={() => setUserPickerOpen(false)}
        onDone={(users) => {
          setUsers(users);
          setUserPickerOpen(false);
        }}
      />
    </>
  );
}
