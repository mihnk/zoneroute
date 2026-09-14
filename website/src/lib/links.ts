import { visit } from 'unist-util-visit';

/**
 * Canonical Markdown links have to work in two places at once: on GitHub,
 * where `coredns-wiring.md` is a file next to the reader, and here, where the
 * same document is a page at `/docs/coredns-wiring`.
 *
 * The Markdown is never rewritten. This rehype plugin translates links while
 * the page is rendered:
 *
 *   coredns-wiring.md              -> /docs/coredns-wiring
 *   install.md#verify              -> /docs/install#verify
 *   docs/install.md                -> /docs/install       (from README)
 *   docs/release.md                -> GitHub blob         (maintainer-only)
 *   LICENSE                        -> GitHub blob
 *   #prerequisites, https://...    -> unchanged
 */

const REPO = 'https://github.com/mihnk/zoneroute';
const BLOB = `${REPO}/blob/main`;

/** Documents this site renders, by their file name without the extension. */
const PAGES = new Set(['install', 'coredns-wiring', 'troubleshooting']);

export function resolveLink(href: string): string {
  if (!href) return href;

  // Absolute URLs, anchors, mail and protocol-relative links are left alone.
  if (/^([a-z][a-z0-9+.-]*:|\/\/|#|\/)/i.test(href)) return href;

  const [path, hash = ''] = href.split('#');
  const fragment = hash ? `#${hash}` : '';

  // A bare fragment was handled above; a path that is not Markdown is a
  // repository file with no page here.
  if (!path.endsWith('.md')) return `${BLOB}/${path}${fragment}`;

  const name = path.replace(/^(\.\/)?(docs\/)?/, '').replace(/\.md$/, '');
  // The trailing slash matches the routes Astro generates, so the link is
  // served directly instead of being redirected.
  if (PAGES.has(name)) return `/docs/${name}/${fragment}`;

  // README.md and docs/release.md are read on GitHub.
  return `${BLOB}/${path}${fragment}`;
}

export function rehypeRepoLinks() {
  return (tree: unknown) => {
    visit(
      tree as never,
      'element',
      (node: { tagName?: string; properties?: Record<string, unknown> }) => {
        if (node.tagName !== 'a' || !node.properties) return;
        const href = node.properties.href;
        if (typeof href !== 'string') return;

        const resolved = resolveLink(href);
        node.properties.href = resolved;

        // Links that leave the site get the usual treatment.
        if (/^https?:/.test(resolved)) {
          node.properties.rel = 'noopener noreferrer';
        }
      },
    );
  };
}
