/**
 * FixedAppPlaceholder — 固定应用的统一占位渲染器。
 *
 * 条码信息查询 / OA账号解锁 / 修改OA密码 / 物料信息查询等固定应用的
 * 前后端业务逻辑分批落地；在此之前，应用中心的每一个 renderer_key 都
 * 先接到这里，保证「跳转 → 使用 → 返回」的完整链路先行可用。
 *
 * 具体页面就绪时，只需在 PageRenderer 的 FIXED_RENDERERS 注册表里把
 * 对应 renderer_key 换成真正的实现，工作台其余部分零改动（§99/§100）。
 */
import React from 'react';
import { Tag } from 'antd';
import { ToolOutlined } from '@ant-design/icons';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import type { V2Application } from '@/services/runApi';
import './FixedAppPlaceholder.css';

interface Props {
  application: V2Application;
}

const KIND_LABELS: Record<string, string> = {
  page: '查询页面',
  form: '业务表单',
  dashboard: '数据看板',
};

const FixedAppPlaceholder: React.FC<Props> = ({ application }) => (
  <div className="fixed-app-placeholder">
    <div className="fixed-app-placeholder__card">
      <AgentAvatar
        application={application}
        size={64}
        shape="square"
        className="fixed-app-placeholder__icon"
        tint={application.color}
      />
      <h2 className="fixed-app-placeholder__name">{application.name}</h2>
      {application.description && (
        <p className="fixed-app-placeholder__desc">{application.description}</p>
      )}
      <Tag className="fixed-app-placeholder__tag" bordered={false}>
        <ToolOutlined /> 功能开发中
      </Tag>
      <p className="fixed-app-placeholder__hint">
        {KIND_LABELS[application.kind] || '业务页面'}正在按企业流程开发，
        发布后会自动出现在应用中心与首页快捷入口。
      </p>
    </div>
  </div>
);

export default FixedAppPlaceholder;
