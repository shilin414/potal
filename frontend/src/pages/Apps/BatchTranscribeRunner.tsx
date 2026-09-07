import React, { useEffect, useState } from 'react';
import { Button, Input, Select, Progress, Table, Spin, Result } from 'antd';
import { ArrowLeftOutlined, FolderOpenOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useAppStore } from '@/stores/useAppStore';
import { useJob } from '@/hooks/useJob';
import { api } from '@/services/api';
import type { AppItem } from '@/types';
import FolderPickerModal from './FolderPickerModal';
import './BatchTranscribeRunner.css';

const SLUG = 'batch-transcribe';

interface BatchTranscribeRunnerProps {
  application?: AppItem;
  embedded?: boolean;
}

const BatchTranscribeRunner: React.FC<BatchTranscribeRunnerProps> = ({ application, embedded }) => {
  const navigate = useNavigate();
  const { loadApp } = useAppStore();
  const { state, start, stop } = useJob(SLUG);

  const [app, setApp] = useState<AppItem | null>(application ?? null);
  const [loadingApp, setLoadingApp] = useState(!application);
  const [folder, setFolder] = useState('');
  const [model, setModel] = useState('base');
  const [language, setLanguage] = useState('zh');
  const [videos, setVideos] = useState<{ name: string }[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [pickerOpen, setPickerOpen] = useState(false);

  useEffect(() => {
    if (application) {
      setApp(application);
      setLoadingApp(false);
      return;
    }
    loadApp(SLUG).then(setApp).finally(() => setLoadingApp(false));
  }, [application, loadApp]);

  const scan = async (p: string = folder) => {
    const target = p.trim();
    if (!target) return;
    const data = await api.post<{ videos: { name: string }[] }>('/app-runner/fs/scan/', { path: target });
    setVideos(data.videos);
  };

  if (loadingApp) return <div className="bt-runner"><Spin size="large" /></div>;
  if (!app) return (
    <div className="bt-runner">
      <Result status="404" title="应用不存在"
        extra={<Button type="primary" onClick={() => navigate('/apps')}>返回应用中心</Button>} />
    </div>
  );

  const isRunning = state.status === 'running';
  const pct = state.progress.total ? Math.round((state.progress.current / state.progress.total) * 100) : 0;

  return (
    <div className="bt-runner animate-fade-in">
      {!embedded && <div className="bt-runner-topbar">
        <Button type="text" icon={<ArrowLeftOutlined />} onClick={() => navigate('/apps')}>
          返回应用中心
        </Button>
        <div className="bt-runner-app"><span>{app.icon}</span><span>{app.name}</span></div>
      </div>}

      <div className="bt-runner-config">
        <div className="bt-runner-row">
          <Input
            placeholder="服务器视频文件夹路径（如 D:\\videos）"
            value={folder}
            onChange={(e) => setFolder(e.target.value)}
            style={{ flex: 1 }}
          />
          <Button icon={<FolderOpenOutlined />} onClick={() => setPickerOpen(true)}>打开</Button>
          <Button onClick={() => scan()}>扫描</Button>
        </div>
        <div className="bt-runner-row">
          <Select value={model} onChange={setModel} style={{ width: 140 }}
            options={['tiny', 'base', 'small', 'medium', 'large'].map((m) => ({ value: m, label: m }))} />
          <Select value={language} onChange={setLanguage} style={{ width: 140 }}
            options={[{ value: 'zh', label: '中文' }, { value: 'en', label: '英文' }]} />
          <Button type="primary" disabled={isRunning || submitting || videos.length === 0}
            onClick={async () => {
              setSubmitting(true);
              try {
                await start({ folder: folder.trim(), model, language });
              } finally {
                setSubmitting(false);
              }
            }}>
            开始转录
          </Button>
          <Button danger disabled={!isRunning} onClick={stop}>停止</Button>
        </div>
        {videos.length > 0 && <div className="bt-runner-hint">找到 {videos.length} 个视频文件</div>}
      </div>

      <div className="bt-runner-progress">
        <Progress percent={pct} status={state.status === 'error' ? 'exception' : isRunning ? 'active' : 'normal'} />
        <span className="bt-runner-status">{state.status}</span>
      </div>

      <Table
        size="small" rowKey="id" pagination={false}
        dataSource={state.items}
        columns={[
          { title: '文件名', dataIndex: 'name' },
          { title: '状态', dataIndex: 'status', width: 100 },
          { title: '输出路径', dataIndex: 'result' },
          { title: '错误', dataIndex: 'error' },
        ]}
      />

      <div className="bt-runner-log">
        {state.logs.map((l, i) => (<div key={i} className={`bt-runner-log-line lvl-${l.level}`}>{l.msg}</div>))}
      </div>

      <FolderPickerModal
        open={pickerOpen}
        onClose={() => setPickerOpen(false)}
        onSelect={(p) => {
          setFolder(p);
          setPickerOpen(false);
          scan(p);
        }}
      />
    </div>
  );
};

export default BatchTranscribeRunner;
