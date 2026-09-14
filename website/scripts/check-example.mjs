#!/usr/bin/env node
/**
 * The homepage shows a ZoneRoute object. It must stay a valid one: every
 * field it uses has to exist in the current v1alpha1 API, and the apiVersion
 * and kind have to match what the CRD serves.
 *
 * The check reads the generated CRD rather than a test fixture, so the
 * example is verified against the API the cluster actually accepts.
 */
import { readFile } from 'node:fs/promises';

const CRD = new URL('../../config/crd/dns.mihnk.org_zoneroutes.yaml', import.meta.url).pathname;
const EXAMPLE = new URL('../src/data/example.yaml', import.meta.url).pathname;

const crd = await readFile(CRD, 'utf8');
const example = await readFile(EXAMPLE, 'utf8');

const fail = (msg) => {
  console.error(`homepage example: ${msg}`);
  process.exitCode = 1;
};

// The CRD names the group, version and kind it serves.
const group = crd.match(/^ {2}group: (\S+)$/m)?.[1];
const version = crd.match(/^ {4}name: (v\S+)$/m)?.[1];
const kind = crd.match(/^ {4}kind: (ZoneRoute)$/m)?.[1];
if (!group || !version || !kind) fail('could not read group/version/kind from the CRD');

const wantApiVersion = `${group}/${version}`;
if (!example.includes(`apiVersion: ${wantApiVersion}`)) {
  fail(`apiVersion must be ${wantApiVersion}`);
}
if (!example.includes(`kind: ${kind}`)) fail(`kind must be ${kind}`);

// Every spec field the example sets must be a property the CRD declares.
const declared = new Set([...crd.matchAll(/^ {12,}(\w+):$/gm)].map((m) => m[1]));
for (const field of ['zones', 'upstreams', 'address', 'port']) {
  if (!declared.has(field)) fail(`the CRD no longer declares spec field "${field}"`);
}

const used = [...example.matchAll(/^\s*-?\s*(\w+):/gm)].map((m) => m[1]);
const allowed = new Set([
  'apiVersion',
  'kind',
  'metadata',
  'name',
  'spec',
  'zones',
  'upstreams',
  'address',
  'port',
]);
for (const field of used) {
  if (!allowed.has(field)) fail(`unknown field "${field}"`);
}

if (process.exitCode) process.exit(1);
console.log(`homepage example validates against ${wantApiVersion} ${kind}`);
