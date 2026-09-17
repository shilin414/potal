/**
 * Artifact reference resolution (pure, testable).
 *
 * Aily embeds generated files as sandbox-relative refs of the shape
 *
 *	artifacts/<artifactName>/<...nested...>/<filename>
 *
 * which mean nothing outside the chat surface until they are mapped to a
 * local artifact id and rewritten to an /open resolver. This module owns
 * that mapping so it can be tested without rendering markdown.
 */

export interface ArtifactRefName {
  artifactId: string;
  name: string;
}

/**
 * Drops a trailing file extension, case-insensitively. Artifact refs and
 * artifact names disagree on whether the extension is present (the provider
 * keeps `name.png`, its sandbox ref may be `artifacts/<name>/…`), so
 * comparing the stems is what makes the two forms match.
 */
export function stripExt(name: string): string {
  const i = name.lastIndexOf('.');
  return i > 0 ? name.slice(0, i).toLowerCase() : name.toLowerCase();
}

/**
 * Maps a markdown image/link target to an artifact's /open URL.
 *
 * Returns null when nothing matches, so the caller renders a placeholder
 * rather than a broken <img> that would flicker into place once
 * artifact.discovered lands.
 *
 * NOTE the capture group is `(.+)`, NOT `([^/]+)`: the earlier
 * single-segment form dropped everything after the first directory, so any
 * nested ref failed to resolve. That is precisely why a multi-image answer
 * showed its first (flat-path) image and not its second.
 */
export function resolveArtifactRef(
  src: string,
  artifacts: ArtifactRefName[] | undefined,
  resolveUrl: (artifactId: string) => string,
): string | null {
  const path = src.replace(/^\.?\/?/, '').split(/[?#]/)[0];

  const match = path.match(/^artifacts?\/(.+)$/i);
  if (match) {
    const segments = match[1].split('/').filter(Boolean);
    const refName = decodeURIComponent(segments[0] ?? '');
    const refFile = decodeURIComponent(segments[segments.length - 1] ?? '');
    const hit = (artifacts || []).find((a) => {
      const full = a.name || '';
      const base = full.split(/[\\/]/).pop() || '';
      return (
        full === refName ||
        full === refFile ||
        base === refName ||
        base === refFile ||
        // Name that still matches after dropping the extension: the
        // provider may strip .png from the ref but keep it in the name.
        stripExt(base) === stripExt(refName) ||
        stripExt(base) === stripExt(refFile) ||
        (full !== '' && full.startsWith(refName))
      );
    });
    if (hit) return resolveUrl(hit.artifactId);
  }

  // Also tolerate a bare filename that matches an artifact name exactly.
  const tail = path.split('/').pop() || '';
  const bare = (artifacts || []).find(
    (a) => a.name && (a.name === tail
      || (a.name || '').split(/[\\/]/).pop() === tail
      || stripExt((a.name || '').split(/[\\/]/).pop() || '') === stripExt(tail)));
  if (bare) return resolveUrl(bare.artifactId);
  return null;
}
