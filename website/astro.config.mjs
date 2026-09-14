// @ts-check
import { defineConfig } from 'astro/config';
import sitemap from '@astrojs/sitemap';
import { rehypeRepoLinks } from './src/lib/links.ts';

export default defineConfig({
  site: 'https://zoneroute.mihnk.org',

  // Pages are emitted as directories, so links carry the slash the server
  // would redirect to anyway.
  trailingSlash: 'always',

  integrations: [sitemap()],

  markdown: {
    // Canonical Markdown links point at repository files; this turns them
    // into the routes this site serves without editing the documents.
    rehypePlugins: [rehypeRepoLinks],
    shikiConfig: {
      themes: { light: 'github-light', dark: 'github-dark' },
      wrap: false,
    },
  },
});
