/**
 * MobilePermissionEditor — 移动端访问权限编辑（开发执行报告 §41–§43）。
 *
 * Full Screen Drawer：访问范围 → 授权部门（Flat Search Picker）→ 授权人员
 * （多选 Picker）。数据模型与桌面完全一致（access_mode / departments /
 * users / include_children），保存走同一个 updateAccess 契约。
 */
import React, { useCallback, useEffect, useState } from 'react';
import { Empty, Radio, Skeleton, Switch, message } from 'antd';
import { CloseOutlined } from '@ant-design/icons';
import {
  enterpriseApi,
  type AccessMode,
  type AccessPolicy,
  type DirectoryDepartment,
} from '../enterpriseApi';
import {
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
  const [policy, setPolicy] = useState<AccessPolicy | null>(null);
  const [deps, setDeps] = useState<DirectoryDepartment[]>([]);
  const [saving, setSaving] = useState(false);
  const [depPickerOpen, setDepPickerOpen] = useState(false);
  const [userPickerOpen, setUserPickerOpen] = useState(false);

  // 与桌面 AccessPage.open 相同的加载契约：权限 + 部门 + 人员。
  useEffect(() => {
    if (!open || !application) return;
    let stale = false;
    setPolicy(null);
    Promise.all([
      enterpriseApi.access(application.id),
      enterpriseApi.departments(),
      enterpriseApi.users({ limit: 100 }),
    ])
      .then(([p, d]) => {
        if (stale) return;
        setPolicy(p);
        setDeps(d);
      })
      .catch(() => message.error('加载权限失败'));
    return () => { stale = true; };
  }, [open, application]);

  const save = useCallback(async () => {
    if (!application || !policy) return;
    setSaving(true);
    try {
      const next = await enterpriseApi.updateAccess(application.id, {
        access_mode: policy.access_mode,
        department_grants: policy.departments.map((d) => ({
          department_id: d.department_id,
          include_children: d.include_children,
        })),
        user_grants: policy.users.map((u) => u.directory_user_id),
      });
      setPolicy(next);
      message.success('访问权限已保存');
      onClose();
    } catch {
      message.error('保存失败，请检查部门和人员是否仍有效');
    } finally {
      setSaving(false);
    }
  }, [application, policy, onClose]);

  const setDepIds = (ids: number[]) => {
    if (!policy) return;
    const old = new Map(policy.departments.map((d) => [d.department_id, d]));
    setPolicy({
      ...policy,
      departments: ids.map((id) => old.get(id) || {
        department_id: id,
        name: deps.find((d) => d.id === id)?.name || '',
        include_children: true,
        covered_users: 0,
      }),
    });
  };

  const setUsers = (users: MobilePickedUser[]) => {
    if (!policy) return;
    const old = new Map(policy.users.map((u) => [u.directory_user_id, u]));
    setPolicy({
      ...policy,
      users: users.map((u) => old.get(u.id) || {
        directory_user_id: u.id,
        name: u.name,
        avatar_url: u.avatar_url,
        departments: u.departments,
      }),
    });
  };

  return (
    <>
      <MobileFullScreenDrawer
        open={open}
        title={`${application?.name ?? ''} 访问权限`}
        actionText="保存"
        actionLoading={saving}
        onAction={() => void save()}
        onClose={onClose}
      >
        {!policy ? (
          <Skeleton active />
        ) : (
          <>
            <MobileSection title="访问范围">
              <Radio.Group
                className="mobile-permission__mode"
                value={policy.access_mode}
                onChange={(e) => setPolicy({
                  ...policy,
                  access_mode: e.target.value as AccessMode,
                })}
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
                            onChange={(checked) => setPolicy({
                              ...policy,
                              departments: policy.departments.map((d) => (
                                d.department_id === grant.department_id
                                  ? { ...d, include_children: checked }
                                  : d)),
                            })}
                          />
                          <button
                            type="button"
                            className="mobile-permission__grant-remove"
                            aria-label={`移除部门 ${grant.name}`}
                            onClick={() => setDepIds(
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
          setDepIds(ids);
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
