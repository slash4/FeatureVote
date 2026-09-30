// Checks on the built bundle (run `npm run build` first; `npm test` does not rebuild).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import path from 'node:path';
import { widgetDir } from './helpers.mjs';

const file = path.join(widgetDir, '../internal/web/static/widget.js');
const code = readFileSync(file, 'utf8');

test('bundle parses as a classic script', () => {
  assert.doesNotThrow(() => new vm.Script(code, { filename: 'widget.js' }));
});

test('bundle stays under 40 KB minified', () => {
  const bytes = Buffer.byteLength(code);
  assert.ok(bytes < 40_000, `bundle is ${bytes} bytes`);
});

test('bundle carries the banner and uses no HTML string sinks', () => {
  assert.match(code, /^\/\*! FeatureVote widget/);
  assert.doesNotMatch(code, /innerHTML|outerHTML|insertAdjacentHTML/);
});

test('bundle does not re-mount when window.FeatureVote already exists', () => {
  // With FeatureVote already defined, boot() must return before touching the DOM.
  const existing = { open() {} };
  const sandbox = { window: { FeatureVote: existing }, document: {} };
  sandbox.window.window = sandbox.window;
  vm.runInNewContext(code, sandbox);
  assert.equal(sandbox.window.FeatureVote, existing);
});
