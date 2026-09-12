/**
 * AgentAvatarModal — 智能体市场「修改头像」。
 *
 * Avatars live on the Application (architecture doc §8: 名称/图标/描述 belong to
 * the product entity). Uploads go to POST /api/v2/applications/{id}/avatar and
 * are served back through an auth-checked endpoint, so the img src needs no
 * MEDIA_URL plumbing. Clearing falls back to the emoji `icon`.
 */
import { useEffect, useState } from 'react';
import { Avatar, Button, Modal, Upload, message } from 'antd';
import { DeleteOutlined, UploadOutlined } from '@ant-design/icons';
import {
  clearAgentAvatar,
  uploadAgentAvatar,
  type ManagedAgent,
} from '@/services/runApi';

/** Mirrors the server-side limits (apps/applications/models.py). */
const ALLOWED_EXTENSIONS = ['png', 'jpg', 'jpeg', 'gif', 'webp'];
const MAX_BYTES = 2 * 1024 * 1024;

interface AgentAvatarModalProps {
  agent: ManagedAgent | null;
  open: boolean;
  onClose: () => void;
  onSaved: (agent: ManagedAgent) => void | Promise<void>;
}

const AgentAvatarModal = ({ agent, open, onClose, onSaved }: AgentAvatarModalProps) => {
  const [file, setFile] = useState<File | null>(null);
  const [preview, setPreview] = useState<string>('');
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (open) {
      setFile(null);
      setPreview('');
    }
  }, [open, agent?.id]);

  useEffect(() => () => {
    if (preview) URL.revokeObjectURL(preview);
  }, [preview]);

  const pickFile = (candidate: File): boolean => {
    const extension = (candidate.name.split('.').pop() || '').toLowerCase();
    if (!ALLOWED_EXTENSIONS.includes(extension)) {
      message.error(`头像仅支持 ${ALLOWED_EXTENSIONS.join(' / ')} 格式`);
      return false;
    }
    if (candidate.size > MAX_BYTES) {
      message.error('头像不能超过 2MB');
      return false;
    }
    if (preview) URL.revokeObjectURL(preview);
    setFile(candidate);
    setPreview(URL.createObjectURL(candidate));
    return false;
  };

  const handleSave = async () => {
    if (!agent || !file) return;
    setSaving(true);
    try {
      const updated = await uploadAgentAvatar(agent.id, file);
      message.success('头像已更新');
      await onSaved(updated);
      onClose();
    } catch (error: any) {
      message.error(
        error?.response?.data?.file?.[0]
        || error?.response?.data?.detail
        || '头像上传失败');
    } finally {
      setSaving(false);
    }
  };

  const handleClear = async () => {
    if (!agent) return;
    setSaving(true);
    try {
      const updated = await clearAgentAvatar(agent.id);
      message.success('已恢复为默认图标');
      await onSaved(updated);
      onClose();
    } catch {
      message.error('清除头像失败');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      title="修改头像"
      open={open}
      onCancel={onClose}
      onOk={handleSave}
      okText="保存"
      cancelText="取消"
      confirmLoading={saving}
      okButtonProps={{ disabled: !file }}
      destroyOnClose
      width={420}
    >
      <div className="agent-avatar-editor">
        <Avatar
          size={96}
          src={preview || agent?.avatar_url || undefined}
          className="agent-avatar-editor__preview"
        >
          {agent?.icon || '🤖'}
        </Avatar>
        <div className="agent-avatar-editor__hint">
          {agent?.name}
          <span>图片会按原图保存，建议使用 1:1 的方形图（≤ 2MB）。</span>
        </div>
        <div className="agent-avatar-editor__actions">
          <Upload
            accept={ALLOWED_EXTENSIONS.map((ext) => `.${ext}`).join(',')}
            showUploadList={false}
            beforeUpload={(candidate) => pickFile(candidate as File)}
          >
            <Button icon={<UploadOutlined />}>
              {file ? '重新选择' : '选择图片'}
            </Button>
          </Upload>
          {agent?.avatar_url ? (
            <Button
              icon={<DeleteOutlined />}
              onClick={handleClear}
              disabled={saving}
            >
              恢复默认图标
            </Button>
          ) : null}
        </div>
        {file ? <div className="agent-avatar-editor__file">{file.name}</div> : null}
      </div>
    </Modal>
  );
};

export default AgentAvatarModal;
