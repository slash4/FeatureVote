import { test } from 'node:test';
import assert from 'node:assert/strict';
import { importTs } from './helpers.mjs';

const { resolveLocale, createTranslator } = await importTs('src/i18n.ts');

test('locale resolution uses the primary subtag', () => {
  assert.equal(resolveLocale(['fr-CA']), 'fr');
  assert.equal(resolveLocale(['zh-Hans']), 'zh');
  assert.equal(resolveLocale(['zh-Hans-CN']), 'zh');
  assert.equal(resolveLocale(['DE_at']), 'de');
  assert.equal(resolveLocale(['it']), 'it');
  assert.equal(resolveLocale(['es-419']), 'es');
});

test('unsupported or missing locale falls back to en', () => {
  assert.equal(resolveLocale(['xx']), 'en');
  assert.equal(resolveLocale(['constructor']), 'en');
  assert.equal(resolveLocale([]), 'en');
  assert.equal(resolveLocale([null, undefined, '']), 'en');
});

test('priority: data-locale, then <html lang>, then navigator.language', () => {
  assert.equal(resolveLocale(['de', 'fr', 'es']), 'de');
  assert.equal(resolveLocale([undefined, 'fr-FR', 'es']), 'fr');
  assert.equal(resolveLocale(['', '  ', 'es-MX']), 'es');
});

test('translator interpolates and falls back', () => {
  const t = createTranslator('de');
  assert.equal(t('launcher'), 'Feature-Ideen');
  assert.equal(t('upvote', { title: '<b>x</b>' }), 'Dafür stimmen: <b>x</b>');
  assert.equal(t('status_under_review'), 'In Prüfung');
  assert.equal(createTranslator('xx')('launcher'), 'Feature ideas');
  assert.equal(createTranslator('en')('counter', { count: 3, max: 120 }), '3 / 120');
});
