#!/usr/bin/env node
/**
 * Checks the built site: every internal link must point at a page that was
 * generated, and every anchor at a heading that exists. Runs against dist/,
 * so it sees exactly what Cloudflare will serve.
 */
import { readdir, readFile } from 'node:fs/promises';
import { join, relative } from 'node:path';

const DIST = new URL('../dist/', import.meta.url).pathname;

async function htmlFiles(dir) {
  const out = [];
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...(await htmlFiles(full)));
    else if (entry.name.endsWith('.html')) out.push(full);
  }
  return out;
}

/** A built file maps to the route a browser would ask for. */
function routeOf(file) {
  const rel = relative(DIST, file);
  return '/' + rel.replace(/index\.html$/, '').replace(/\.html$/, '');
}

const files = await htmlFiles(DIST);
const routes = new Set(files.map(routeOf).map((r) => (r.endsWith('/') ? r : r + '/')));
const anchors = new Map();
const pages = new Map();

for (const file of files) {
  const html = await readFile(file, 'utf8');
  const route = routeOf(file);
  pages.set(route, html);
  anchors.set(
    route.endsWith('/') ? route : route + '/',
    new Set([...html.matchAll(/\sid="([^"]+)"/g)].map((m) => m[1])),
  );
}

let failures = 0;
for (const [route, html] of pages) {
  const from = route.endsWith('/') ? route : route + '/';
  for (const [, href] of html.matchAll(/href="([^"]+)"/g)) {
    if (!href.startsWith('/') && !href.startsWith('#')) continue; // external

    // Assets are not routes: the sitemap, stylesheets, images and the like
    // are served as files and have no page to check anchors against.
    if (/\.[a-z0-9]+$/i.test(href.split('#')[0])) continue;

    const [path, hash] = href.split('#');
    const target = path === '' ? from : path.endsWith('/') ? path : path + '/';

    if (path !== '' && !routes.has(target)) {
      console.error(`${route}: link to ${href} — no such page`);
      failures++;
      continue;
    }
    if (hash && !anchors.get(target)?.has(hash)) {
      console.error(`${route}: link to ${href} — no such anchor`);
      failures++;
    }
  }
}

console.log(`checked ${pages.size} pages, ${routes.size} routes`);
if (failures > 0) {
  console.error(`${failures} broken link(s)`);
  process.exit(1);
}
console.log('all internal links resolve');
