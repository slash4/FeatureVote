import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync } from 'node:fs';
import path from 'node:path';
import { importTs, widgetDir } from './helpers.mjs';

const EXPECTED = ['de', 'en', 'es', 'fr', 'it', 'zh'];

test('all six locales exist', () => {
  const files = readdirSync(path.join(widgetDir, 'src/locales')).map((f) => f.replace(/\.ts$/, '')).sort();
  assert.deepEqual(files, EXPECTED);
});

test('every locale has exactly the English keys and no empty strings', async () => {
  const en = (await importTs('src/locales/en.ts')).default;
  const enKeys = Object.keys(en).sort();
  for (const code of EXPECTED) {
    const msgs = (await importTs(`src/locales/${code}.ts`)).default;
    assert.deepEqual(Object.keys(msgs).sort(), enKeys, `${code}: key set differs from en`);
    for (const [k, v] of Object.entries(msgs)) {
      assert.equal(typeof v, 'string', `${code}.${k} is not a string`);
      assert.ok(v.trim().length > 0, `${code}.${k} is empty`);
    }
  }
});

test('placeholders match English in every locale', async () => {
  const en = (await importTs('src/locales/en.ts')).default;
  const vars = (s) => (s.match(/\{\w+\}/g) || []).sort();
  for (const code of EXPECTED) {
    const msgs = (await importTs(`src/locales/${code}.ts`)).default;
    for (const k of Object.keys(en)) assert.deepEqual(vars(msgs[k]), vars(en[k]), `${code}.${k} placeholders`);
  }
});

test('translations are not just copies of English (except shared tokens)', async () => {
  const en = (await importTs('src/locales/en.ts')).default;
  // Keys that are legitimately identical across languages.
  const same = new Set(['counter', 'filterLabel', 'formBody']);
  for (const code of EXPECTED.filter((c) => c !== 'en')) {
    const msgs = (await importTs(`src/locales/${code}.ts`)).default;
    const copied = Object.keys(en).filter((k) => !same.has(k) && msgs[k] === en[k]);
    assert.deepEqual(copied, [], `${code}: untranslated keys`);
  }
});
