import { Empty } from 'antd';
import type { AppItem, ApplicationRuntime as ApplicationRuntimeData } from '@/types';
import ImageGenieRunner from '@/pages/Apps/ImageGenieRunner';
import BatchTranscribeRunner from '@/pages/Apps/BatchTranscribeRunner';
import ChatApplicationView from './ChatApplicationView';

interface Props {
  application: ApplicationRuntimeData;
  projectId?: number;
  workflowStepRunId?: string;
}

const toAppItem = (application: ApplicationRuntimeData): AppItem => ({
  id: application.application_slug,
  applicationId: application.id,
  name: application.application_name,
  description: application.application_description,
  category: '',
  icon: application.application_icon,
  color: application.application_color,
  tags: [],
  kind: application.kind,
  rendererKey: application.renderer_key,
  runtime: application,
});

const ApplicationRuntime: React.FC<Props> = ({ application: runtime, projectId, workflowStepRunId }) => {
  const application = toAppItem(runtime);
  switch (runtime.renderer_key) {
    case 'chat':
      return <ChatApplicationView application={runtime} projectId={projectId}
        workflowStepRunId={workflowStepRunId} />;
    case 'image-genie':
      return <ImageGenieRunner application={application} embedded projectId={projectId} />;
    case 'batch-transcribe':
      return <BatchTranscribeRunner application={application} embedded />;
    default:
      return (
        <div style={{ display: 'grid', height: '100%', placeItems: 'center' }}>
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={`${runtime.application_name} 尚未注册界面：${runtime.renderer_key}`} />
        </div>
      );
  }
};

export default ApplicationRuntime;
