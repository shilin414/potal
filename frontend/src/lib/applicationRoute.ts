/**
 * applicationRoute — the ONE place that turns an Application into a URL.
 *
 * Before this existed the mapping was duplicated: HomeWorkspace inlined
 * `kind === 'chat' ? /chat : /app`, PageRenderer's 返回工作台 inlined a
 * three-way variant, and the mobile selectors were about to add a third. A new
 * renderer kind would then have been wired in some of them and forgotten in
 * the rest — the kind of drift that shows up as a dead shortcut.
 *
 * It takes the DISPLAY projection, not a full catalog item: the mobile
 * surfaces route from bootstrap summaries (执行报告 §9–§11), and every field
 * this decision needs (kind, renderer_key, slug) is part of it.
 *
 * Keep this in sync with `router/index.tsx`:
 *   /chat/:applicationSlug      → ChatRenderer
 *   /app/:applicationSlug       → PageRenderer
 *   /workflow/:applicationSlug  → WorkflowRenderer
 */
import type { ApplicationSummary } from '@/services/runApi';

/** Route path for an application, matching WorkspaceHost's renderer choice. */
export function routeForApplication(application: ApplicationSummary): string {
  // A chat application is always a chat workspace whichever `kind` a legacy
  // row carries — WorkspaceHost decides the same way, on kind OR renderer_key.
  if (application.kind === 'chat' || application.renderer_key === 'chat') {
    return `/chat/${application.slug}`;
  }
  if (application.kind === 'workflow') {
    return `/workflow/${application.slug}`;
  }
  return `/app/${application.slug}`;
}
