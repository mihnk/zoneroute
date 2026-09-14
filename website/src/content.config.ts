import { defineCollection } from 'astro:content';
import { glob } from 'astro/loaders';

/**
 * The canonical documentation lives at the repository root, next to the code
 * it describes, and is read from there at build time. There is no copy of it
 * under website/: one file is rendered both on GitHub and here.
 *
 * docs/release.md is deliberately absent — it is a maintainer document, and
 * links to it resolve to GitHub (see src/lib/links.ts).
 */
const docs = defineCollection({
  loader: glob({
    pattern: ['install.md', 'coredns-wiring.md', 'troubleshooting.md'],
    base: '../docs',
  }),
});

export const collections = { docs };
