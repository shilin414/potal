/**
 * WorkflowRenderer — workflow applications inside the same shell (§18/§98).
 *
 * Routed by `/workflow/:applicationSlug`. The concrete run surface still comes
 * from the renderer registry, so an Aily workflow application (§97) and a
 * local workflow application share this entry point.
 */
import React from 'react';
import type { V2Application } from '@/services/runApi';
import PageRenderer from './PageRenderer';

interface Props {
  application: V2Application;
}

const WorkflowRenderer: React.FC<Props> = ({ application }) => (
  <PageRenderer application={application} kindLabel="工作流" />
);

export default WorkflowRenderer;
