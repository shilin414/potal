/**
 * MobileAttachmentSheet — the `+` menu (design report §10).
 *
 * It contains EXACTLY ONE entry, and that is a decision rather than a
 * placeholder: the report is explicit that a mobile first-level menu should
 * carry fewer options rather than be padded to fill a grid with dead entries
 * (§10 原因). Adding 云文档 / 连接器 / 浏览器 here without a real capability
 * behind them is precisely the "无功能按钮" the acceptance list forbids (§21).
 *
 * The picker itself is the caller's hidden <input type="file">: this sheet only
 * forwards the tap, which keeps the upload chain (validateAttachment →
 * uploadAttachment → pendingUploads) in exactly one place (RunChatPanel).
 */
import React from 'react';
import { Drawer } from 'antd';
import { PaperClipOutlined } from '@ant-design/icons';
import './MobileSheets.css';
import './MobileAttachmentSheet.css';

export interface MobileAttachmentSheetProps {
  open: boolean;
  onPickFiles: () => void;
  onClose: () => void;
}

const MobileAttachmentSheet: React.FC<MobileAttachmentSheetProps> = ({
  open,
  onPickFiles,
  onClose,
}) => (
  <Drawer
    placement="bottom"
    open={open}
    onClose={onClose}
    height="auto"
    closable={false}
    title={null}
    rootClassName="mobile-bottom-sheet"
    styles={{
      content: { borderRadius: '24px 24px 0 0' },
      body: { padding: 0 },
    }}
  >
    <div className="mobile-sheet">
      <div className="mobile-sheet__handle" aria-hidden />
      <div className="mobile-sheet__header">
        <span className="mobile-sheet__title">添加图片 / 文件</span>
      </div>
      <div className="mobile-attachment-sheet__body">
        <button
          type="button"
          className="mobile-attachment-sheet__item"
          onClick={() => {
            // Close first: the OS file picker takes over the viewport, and a
            // sheet left open underneath it would be stale on return.
            onClose();
            onPickFiles();
          }}
        >
          <PaperClipOutlined className="mobile-attachment-sheet__icon" />
          图片 / 文件
        </button>
        <p className="mobile-attachment-sheet__hint">
          支持 png / jpg / pdf，单次最多 8 个；图片不超过 5MB，其他文件不超过 40MB
        </p>
      </div>
    </div>
  </Drawer>
);

export default MobileAttachmentSheet;
