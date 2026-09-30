// Static guards over src/ui.ts (pragmatic heuristics, not a parser):
//  1. No innerHTML / outerHTML / insertAdjacentHTML: DOM is built with createElement + textContent.
//  2. No user-facing English literals. Every string literal is extracted (comments skipped). A literal
//     is flagged when it
//       - contains whitespace, unless every whitespace-separated token is an `fv-` CSS class
//         (class lists like 'fv-btn fv-primary' are the only multi-word literals allowed), or
//       - is a single Capitalised word (e.g. 'Upgrade'), except DOM key names like 'Escape'/'Tab'.
//     Template literals are checked with their ${...} parts removed. Icon path data lives in icons.ts.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { widgetDir } from './helpers.mjs';

const src = readFileSync(path.join(widgetDir, 'src/ui.ts'), 'utf8');

function literals(code) {
  const out = [];
  let i = 0;
  while (i < code.length) {
    const c = code[i];
    if (c === '/' && code[i + 1] === '/') {
      i = code.indexOf('\n', i);
      if (i < 0) break;
    } else if (c === '/' && code[i + 1] === '*') {
      i = code.indexOf('*/', i + 2) + 2;
    } else if (c === '"' || c === "'" || c === '`') {
      let j = i + 1;
      let s = '';
      while (j < code.length && code[j] !== c) {
        if (code[j] === '\\') {
          s += code[j + 1];
          j += 2;
          continue;
        }
        s += code[j++];
      }
      out.push(c === '`' ? s.replace(/\$\{[^}]*\}/g, '') : s);
      i = j + 1;
    } else if (c === '/' && /[=(,:!&|?]\s*$/.test(code.slice(Math.max(0, i - 3), i))) {
      // regex literal: skip to the closing unescaped slash
      let j = i + 1;
      while (j < code.length && code[j] !== '/') j += code[j] === '\\' ? 2 : 1;
      i = j + 1;
    } else i++;
  }
  return out;
}

const KEY_NAMES = new Set(['Escape', 'Esc', 'Tab', 'Enter']);

function looksLikeProse(s) {
  if (/\s/.test(s.trim()) && /[A-Za-z]/.test(s)) {
    return !s.trim().split(/\s+/).every((tok) => /^fv-[a-z0-9_-]*$/.test(tok));
  }
  return /^[A-Z][a-z]{2,}[.!?]?$/.test(s.trim()) && !KEY_NAMES.has(s.trim());
}

test('ui.ts never uses HTML string sinks', () => {
  assert.doesNotMatch(src, /innerHTML|outerHTML|insertAdjacentHTML|document\.write/);
});

test('ui.ts has no user-facing literal strings (all text goes through t())', () => {
  const lits = literals(src);
  assert.ok(lits.length > 50, 'literal extraction looks broken');
  const flagged = lits.filter(looksLikeProse);
  assert.deepEqual(flagged, []);
});

test('guard heuristic catches prose', () => {
  assert.ok(looksLikeProse('Log in to vote'));
  assert.ok(looksLikeProse('something went wrong'));
  assert.ok(looksLikeProse('Upgrade'));
  assert.ok(!looksLikeProse('fv-btn fv-primary'));
  assert.ok(!looksLikeProse('aria-label'));
  assert.ok(!looksLikeProse('Escape'));
});
