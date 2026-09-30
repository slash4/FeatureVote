import { test } from 'node:test';
import assert from 'node:assert/strict';
import { importTs } from './helpers.mjs';

const { TokenManager, decodeExp } = await importTs('src/token.ts');
const { Api } = await importTs('src/api.ts');

const b64u = (o) => Buffer.from(JSON.stringify(o)).toString('base64url');
const jwt = (payload) => b64u({ alg: 'HS256', typ: 'JWT' }) + '.' + b64u(payload) + '.sig';

test('decodeExp reads exp without verifying', () => {
  assert.equal(decodeExp(jwt({ sub: 'ü', exp: 1700000000 })), 1700000000);
  assert.equal(decodeExp('garbage'), null);
  assert.equal(decodeExp('a.b.c'), null);
  assert.equal(decodeExp(jwt({ sub: 'x' })), null);
});

test('token is reused until 60s before exp, then refreshed', async () => {
  let now = 1_000_000_000_000;
  let calls = 0;
  const exp = now / 1000 + 600; // 10 minute token
  const tm = new TokenManager(null, () => (calls++, jwt({ exp })), () => now);
  assert.ok(await tm.get());
  await tm.get();
  assert.equal(calls, 1);
  now += 539_000; // 61s before exp
  await tm.get();
  assert.equal(calls, 1);
  now += 2_000; // 59s before exp
  await tm.get();
  assert.equal(calls, 2);
  tm.invalidate();
  await tm.get();
  assert.equal(calls, 3);
});

test('provider returning null means anonymous', async () => {
  const tm = new TokenManager('/ignored', async () => null);
  assert.equal(await tm.get(), null);
});

test('token url: 200 {token} is used, 401 means anonymous', async (t) => {
  const orig = globalThis.fetch;
  t.after(() => (globalThis.fetch = orig));
  let status = 200;
  const seen = [];
  globalThis.fetch = async (url, init) => {
    seen.push([url, init.credentials]);
    return new Response(JSON.stringify({ token: 'tok.en.x' }), { status });
  };
  assert.equal(await new TokenManager('/api/fv-token', null).get(), 'tok.en.x');
  assert.deepEqual(seen[0], ['/api/fv-token', 'include']);
  status = 401;
  assert.equal(await new TokenManager('/api/fv-token', null).get(), null);
  globalThis.fetch = async () => {
    throw new TypeError('network');
  };
  assert.equal(await new TokenManager('/api/fv-token', null).get(), null);
});

test('api retries once with a fresh token after a 401', async (t) => {
  const orig = globalThis.fetch;
  t.after(() => (globalThis.fetch = orig));
  let n = 0;
  const tm = new TokenManager(null, () => 'tok' + ++n);
  const auths = [];
  globalThis.fetch = async (url, init) => {
    auths.push(init.headers.Authorization);
    if (auths.length === 1)
      return new Response(JSON.stringify({ error: { code: 'token_expired', message: 'x' } }), { status: 401 });
    return new Response(JSON.stringify({ voter: true, votes: [], ideas: [] }), { status: 200 });
  };
  const api = new Api('https://fv.example/', tm);
  const res = await api.me();
  assert.equal(res.ok, true);
  assert.deepEqual(auths, ['Bearer tok1', 'Bearer tok2']);
});

test('api maps error codes, Retry-After and network failures', async (t) => {
  const orig = globalThis.fetch;
  t.after(() => (globalThis.fetch = orig));
  const api = new Api('https://fv.example', new TokenManager(null, () => 'tok'));
  let urls = [];
  globalThis.fetch = async (url) => {
    urls.push(url);
    return new Response(JSON.stringify({ error: { code: 'rate_limited', message: 'slow down' } }), {
      status: 429,
      headers: { 'Retry-After': '42' },
    });
  };
  const res = await api.vote(7, 1);
  assert.deepEqual([res.ok, res.status, res.code, res.retryAfter], [false, 429, 'rate_limited', 42]);
  assert.equal(urls[0], 'https://fv.example/v1/ideas/7/vote');
  await api.listIdeas('new', 'planned', 50);
  assert.equal(urls[1], 'https://fv.example/v1/ideas?sort=new&limit=50&offset=50&status=planned');
  globalThis.fetch = async () => {
    throw new TypeError('offline');
  };
  assert.equal((await api.listIdeas('top', '', 0)).code, 'network');
  // No token: authenticated calls short-circuit as unauthorized without hitting the network.
  const anon = new Api('https://fv.example', new TokenManager(null, () => null));
  assert.equal((await anon.me()).code, 'unauthorized');
});
