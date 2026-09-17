/**
 * Mobile surfaces (design report §3).
 *
 * Everything here is mobile-only presentation: the desktop shell keeps
 * `ApplicationSwitcher` and `HomeShortcuts` untouched, and WorkspaceHost / Run
 * v2 / the SSE protocol are shared between the two shells (§17).
 */
export { default as MobileHomeSurface } from './MobileHomeSurface';
export { default as MobileCatalogSheet } from './MobileCatalogSheet';
export { default as MobileAgentSwitcher } from './MobileAgentSwitcher';
export { default as MobileComposer } from './MobileComposer';
export type { MobileComposerProps, PendingUpload } from './MobileComposer';
export { default as MobileSkillSheet } from './MobileSkillSheet';
export { default as MobileAttachmentSheet } from './MobileAttachmentSheet';
