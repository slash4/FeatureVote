# FeatureVote host integration contract

This document is the contract between FeatureVote and a **host product** (Okokumo, DoLoop, …).
Follow it verbatim. Every code sample here is exercised by the FeatureVote test suite
(`internal/hosttoken/example_test.go`, `scripts/verify-ruby-mint.rb`).

Contents

1. [Overview and deployment model](#1-overview-and-deployment-model)
2. [Host token (JWT)](#2-host-token-jwt)
3. [Host token endpoint](#3-host-token-endpoint)
4. [Minting code: Go and Ruby](#4-minting-code)
5. [Embedding the widget](#5-embedding-the-widget)
6. [Host environment variables](#6-host-environment-variables)
7. [Service configuration (FV_*)](#7-service-configuration-fv_)
8. [CORS and CSP](#8-cors-and-csp)
9. [Administration](#9-administration)
10. [GDPR hook](#10-gdpr-hook)
11. [API reference](#11-api-reference)

---

## 1. Overview and deployment model

FeatureVote is a small feature-voting service: a Go HTTP service + Postgres + an embeddable
floating widget served by the service itself at `/widget.js`.

- **One instance per host product.** Okokumo and DoLoop each run their own FeatureVote with its own
  database, domain (e.g. `https://feedback.okokumo.com`), secrets and config. It is not multi-tenant.
- **The host owns identity and eligibility.** FeatureVote never sees passwords, sessions, emails or
  names. The host mints a short-lived signed token (section 2) that says "this opaque user id may
  (or may not) vote". FeatureVote stores only that opaque id.
- **Moderation first.** Every user-submitted idea starts `pending` and is invisible to the public until an
  admin approves it. Authors are always anonymous publicly.
- **Voting rules.** One vote per (idea, user), value `+1` or `-1`, changeable and removable, never
  stacking. Users cannot vote on their own ideas. Voting is closed on `shipped` / `declined` ideas.
- **Statuses:** `under_review` (default on approval), `planned`, `in_progress`, `shipped`, `declined`.

```
 browser ──(1) GET /api/feature-vote/token (host session/bearer)──▶ host backend
         ◀──────────── {"token":"<jwt>"} ─────────────────────────
 browser ──(2) /v1/... Authorization: Bearer <jwt> ───────────────▶ FeatureVote
```

## 2. Host token (JWT)

A compact JWS, **HS256 only**, signed with the shared secret (`FV_HOST_SECRET` on the service =
`FEATURE_VOTE_SECRET` on the host).

Header (exactly):

```json
{"alg":"HS256","typ":"JWT"}
```

`typ` is optional; if present it must be `"JWT"`. Any other `alg` (`none`, `HS512`, `RS256`, …) is rejected.

Claims:

| Claim   | Type          | Required | Constraint | What FeatureVote does with it |
|---------|---------------|----------|------------|-------------------------------|
| `iss`   | string        | yes      | must equal the instance's `FV_HOST_ISSUER` (e.g. `"okokumo"`, `"doloop"`) | rejects tokens from another product |
| `sub`   | string        | yes      | non-empty, ≤ 255 bytes, **opaque stable user id** — never an email or a name | the only identity stored (`votes.voter_sub`, `ideas.author_sub`) |
| `voter` | JSON boolean  | yes*     | the **host's eligibility decision** (e.g. paid plan) | `true`: may vote and submit. `false`/missing/non-boolean: read-only (`403 not_eligible` on writes) |
| `iat`   | number (unix seconds) | recommended | must not be in the future (beyond skew) | sanity check only |
| `exp`   | number (unix seconds) | yes | `exp − now ≤ 15 min` (plus skew) | token rejected once expired |

\* a missing `voter` is treated as `false`.

Validation (`FV_CLOCK_SKEW`, default 30 s):

- signature: HMAC-SHA256 over `base64url(header) + "." + base64url(payload)`, constant-time compare;
- expired if `now > exp + skew` → `401 token_expired`;
- rejected if `exp > now + 15m + skew` (host minted a too long-lived token) → `401 invalid_token`;
- rejected if `iat` or `nbf` is present and `> now + skew` → `401 invalid_token`;
- anything else malformed → `401 invalid_token`. The token is never logged.

**Recommended TTL: 10 minutes.** The widget refreshes the token 60 s before `exp` and, on a `401`,
re-fetches once and retries once.

## 3. Host token endpoint

Each host exposes one endpoint (path is the host's choice; Okokumo uses `/api/feature-vote/token`):

```
GET <host>/api/feature-vote/token
```

| Condition | Response |
|-----------|----------|
| user authenticated by the host's normal auth (session cookie or `Authorization: Bearer`) | `200 {"token":"<jwt>"}` |
| not logged in | `401` (body irrelevant) |
| FeatureVote not configured (`FEATURE_VOTE_URL` or `FEATURE_VOTE_SECRET` unset) | `404` |

Always send `Cache-Control: no-store`. Mint a fresh token per request with TTL 10 minutes. `sub` is the
user's stable internal id (a UUID or numeric id rendered as a string); `voter` is whatever rule the
product uses ("has a paid plan", or simply `true` for DoLoop).

The widget calls this endpoint with `credentials: 'include'`; a `401`/`403`/`404` or network error makes it
fall back to anonymous read-only mode, so the endpoint is safe to leave unconfigured.

The reference implementation is `cmd/demohost` (`GET /api/fv-token`, cookie session).

## 4. Minting code

### Go (stdlib only)

Used by Okokumo (Go/Echo) and DoLoop `api-go`. This exact function is compiled and verified against the
service's verifier in `internal/hosttoken/example_test.go`.

```go
import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"time"
)

// MintFeatureVoteToken returns a short-lived HS256 JWT identifying the
// current user to FeatureVote. sub must be an opaque, stable user id (never an
// email); voter is the host's eligibility decision.
func MintFeatureVoteToken(secret, issuer, sub string, voter bool, ttl time.Duration) string {
	enc := base64.RawURLEncoding
	now := time.Now()
	header := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iss":   issuer,
		"sub":   sub,
		"voter": voter,
		"iat":   now.Unix(),
		"exp":   now.Add(ttl).Unix(),
	})
	signingInput := header + "." + enc.EncodeToString(claims)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	return signingInput + "." + enc.EncodeToString(mac.Sum(nil))
}
```

`net/http` handler:

```go
// GET /api/feature-vote/token — mount behind the host's normal auth middleware.
func FeatureVoteTokenHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	baseURL, secret := os.Getenv("FEATURE_VOTE_URL"), os.Getenv("FEATURE_VOTE_SECRET")
	if baseURL == "" || secret == "" {
		http.NotFound(w, r) // FeatureVote not configured for this deployment
		return
	}
	issuer := os.Getenv("FEATURE_VOTE_ISSUER")
	if issuer == "" {
		issuer = "okokumo" // product name
	}
	user, ok := currentUser(r) // the host's own session/bearer lookup
	if !ok {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	token := MintFeatureVoteToken(secret, issuer, user.ID, user.HasPaidPlan(), 10*time.Minute)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": token})
}
```

Echo (Okokumo) equivalent:

```go
e.GET("/api/feature-vote/token", func(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	baseURL, secret := os.Getenv("FEATURE_VOTE_URL"), os.Getenv("FEATURE_VOTE_SECRET")
	if baseURL == "" || secret == "" {
		return c.NoContent(http.StatusNotFound)
	}
	user, ok := currentUser(c) // from the session cookie
	if !ok {
		return c.NoContent(http.StatusUnauthorized)
	}
	issuer := os.Getenv("FEATURE_VOTE_ISSUER")
	if issuer == "" {
		issuer = "okokumo"
	}
	return c.JSON(http.StatusOK, map[string]string{
		"token": MintFeatureVoteToken(secret, issuer, user.ID, user.HasPaidPlan(), 10*time.Minute),
	})
})
```

### Ruby (`jwt` gem)

Used by the DoLoop Rails `api/`. Verified by `scripts/verify-ruby-mint.rb` (mints with `jwt` and checks the
token with `fvtoken -verify`).

```ruby
require "jwt"

module FeatureVoteToken
  TTL = 600 # seconds; FeatureVote rejects tokens living longer than 15 minutes

  # sub: opaque, stable user id (never an email). voter: the host's eligibility decision.
  def self.mint(secret:, issuer:, sub:, voter:, ttl: TTL)
    now = Time.now.to_i
    payload = { iss: issuer, sub: sub.to_s, voter: voter == true, iat: now, exp: now + ttl }
    JWT.encode(payload, secret, "HS256")
  end
end
```

Rails controller action:

```ruby
# config/routes.rb:  get "api/feature-vote/token", to: "feature_vote#token"
class FeatureVoteController < ApplicationController
  before_action :authenticate_user! # the app's normal auth (bearer token for DoLoop)

  def token
    response.headers["Cache-Control"] = "no-store"
    secret = ENV["FEATURE_VOTE_SECRET"]
    return head :not_found if ENV["FEATURE_VOTE_URL"].blank? || secret.blank?

    token = FeatureVoteToken.mint(
      secret: secret,
      issuer: ENV.fetch("FEATURE_VOTE_ISSUER", "doloop"),
      sub: current_user.id.to_s,
      voter: true # DoLoop: every signed-in user may vote
    )
    render json: { token: token }
  end
end
```

(`authenticate_user!` must return `401` for anonymous requests, not redirect to a login page.)

## 5. Embedding the widget

The service serves the bundle at `https://<fv-host>/widget.js` (5-minute cache, ETag). Load it once per page,
with `defer`. The widget renders a floating button in a Shadow DOM and defines one global, `window.FeatureVote`.

### Cookie-session host (Okokumo)

The token endpoint is authenticated by the session cookie, so `data-token-url` is enough:

```html
<script src="https://feedback.okokumo.com/widget.js"
        data-token-url="/api/feature-vote/token"
        data-login-url="/login"
        data-upgrade-url="/billing"
        data-locale="fr"
        data-accent="#4f46e5"
        defer></script>
```

`data-token-url` is fetched with `credentials: 'include'` from the page's origin (relative URLs resolve
against the page). SvelteKit: put the tag in `src/app.html` or render it in the root `+layout.svelte`
only when the server-side config says FeatureVote is enabled.

### Bearer-token SPA host (DoLoop)

The SPA keeps its access token in `localStorage`, so cookies cannot authenticate the token endpoint. Provide a
token function instead; it **wins over** `data-token-url`:

```html
<script>
  window.FeatureVoteConfig = {
    // Return the FeatureVote JWT, or null for anonymous (read-only).
    getToken: async () => {
      const access = localStorage.getItem('access_token');
      if (!access) return null;
      const res = await fetch('/api/feature-vote/token', { headers: { Authorization: 'Bearer ' + access } });
      if (!res.ok) return null;
      return (await res.json()).token;
    },
  };
</script>
<script src="https://feedback.doloop.app/widget.js"
        data-login-url="/login"
        data-upgrade-text="Voting is available to signed-in users."
        defer></script>
```

If the provider is only available later (e.g. after the Vue app boots or the user logs in/out):

```js
window.FeatureVote.setTokenProvider(async () => store.fvToken());  // re-renders with the new identity
window.FeatureVote.setTokenProvider(null);                           // back to anonymous
```

### Read-only public page (marketing site)

No token source at all: everyone is anonymous, sees approved ideas and a "log in to vote" link.

```html
<script src="https://feedback.okokumo.com/widget.js" data-login-url="https://app.okokumo.com/login" defer></script>
```

### States

| Identity | UI |
|----------|----|
| no token (anonymous) | read-only list + "log in to vote" CTA → `data-login-url` (no link if unset) |
| token with `voter:false` | read-only list + "voting is for paid plans" CTA → `data-upgrade-url` (text overridable) |
| token with `voter:true` | vote buttons + "Suggest an idea" form + "my ideas" (pending ones marked) |

### `data-*` attributes

| Attribute | Default | Meaning |
|-----------|---------|---------|
| `data-api` | origin of the script `src` | FeatureVote base URL (only needed if the script is served from elsewhere) |
| `data-token-url` | — | host token endpoint (section 3); fetched with `credentials:'include'` |
| `data-login-url` | — | where anonymous users are sent; CTA link hidden if unset |
| `data-login-text` | localized "Log in to vote and suggest ideas" | override the anonymous CTA sentence |
| `data-login-link-text` | localized "Log in" | override the anonymous CTA link label |
| `data-upgrade-url` | — | where `voter:false` users are sent; link hidden if unset |
| `data-upgrade-text` | localized "Voting is available on paid plans" | override the `voter:false` CTA sentence (DoLoop has no paid plan) |
| `data-upgrade-link-text` | localized "Upgrade" | override the `voter:false` CTA link label |
| `data-locale` | `<html lang>`, then `navigator.language`, then `en` | one of `en` `fr` `de` `es` `it` `zh` |
| `data-accent` | `#4f46e5` | accent colour (CSS hex) |
| `data-position` | `bottom-right` | `bottom-right`, `bottom-left`, `top-right`, `top-left` |

### JavaScript API

| Call | Effect |
|------|--------|
| `FeatureVote.open()` | open the panel (e.g. from a "Feedback" menu item) |
| `FeatureVote.close()` | close the panel |
| `FeatureVote.toggle()` | toggle the panel |
| `FeatureVote.refresh()` | drop the cached token and reload ideas and identity (call after login/logout/plan change) |
| `FeatureVote.setTokenProvider(fn)` | set/replace (or `null` to clear) the async token provider |

## 6. Host environment variables

Product repos use these names:

| Variable | Meaning |
|----------|---------|
| `FEATURE_VOTE_URL` | base URL of the product's FeatureVote instance, e.g. `https://feedback.okokumo.com` (used to build the `<script src>`) |
| `FEATURE_VOTE_SECRET` | shared HMAC secret; **must equal** the instance's `FV_HOST_SECRET` (≥ 32 bytes) |
| `FEATURE_VOTE_ISSUER` | optional; defaults to the product name (`okokumo`, `doloop`); must equal `FV_HOST_ISSUER` |

When `FEATURE_VOTE_URL` or `FEATURE_VOTE_SECRET` is unset: the token endpoint returns `404`, the widget
`<script>` is not injected, and **boot and build never fail**. FeatureVote is always optional for the host.

## 7. Service configuration (`FV_*`)

Environment only; the service refuses to start with a clear message on any invalid value.

| Variable | Default | Notes |
|----------|---------|-------|
| `FV_DATABASE_URL` | **required** | Postgres URL; migrations run at boot |
| `FV_HOST_SECRET` | **required** | ≥ 32 bytes; same value as the host's `FEATURE_VOTE_SECRET` |
| `FV_HOST_ISSUER` | **required** | expected `iss`, e.g. `okokumo` |
| `FV_ADMIN_TOKEN` | **required** | ≥ 32 bytes, must differ from `FV_HOST_SECRET` |
| `FV_ALLOWED_ORIGINS` | empty | comma-separated exact origins allowed to call `/v1/*` from browsers |
| `FV_LISTEN_ADDR` | `:8080` | |
| `FV_SUBMIT_LIMIT_PER_DAY` | `5` | ideas per user per trailing 24 h (counted in the database) |
| `FV_VOTE_LIMIT_PER_MINUTE` | `30` | vote PUT+DELETE per user per minute (in-memory, per process) |
| `FV_CLOCK_SKEW` | `30s` | Go duration, max `2m` |
| `FV_COOKIE_SECURE` | `true` | set `false` only for plain-HTTP local dev (admin page cookie) |

Generate secrets with `openssl rand -hex 32`.

## 8. CORS and CSP

**CORS.** Add every origin that embeds the widget to `FV_ALLOWED_ORIGINS` — typically both the app and
the marketing site:

```
FV_ALLOWED_ORIGINS=https://app.okokumo.com,https://okokumo.com
```

Allowed origins get `Access-Control-Allow-Origin: <origin>` (never `*`, never credentials) on `/v1/*`.
Preflights from other origins get `403 forbidden_origin`. `/widget.js` is served with
`Access-Control-Allow-Origin: *`. The admin API has no CORS at all.

**CSP.** If the host sends a Content-Security-Policy, the FeatureVote origin must be allowed for the script
and for API calls; the widget injects its styles into its own Shadow DOM:

```
script-src  'self' https://feedback.okokumo.com;
connect-src 'self' https://feedback.okokumo.com;
style-src   'self' 'unsafe-inline';
```

With a nonce-based `script-src`, add the nonce to the `<script src=".../widget.js">` tag instead. The
host token endpoint is same-origin, so it is covered by `connect-src 'self'`.

## 9. Administration

All admin calls use `Authorization: Bearer $FV_ADMIN_TOKEN`. Missing/wrong token → `401 unauthorized`.

```sh
FV=https://feedback.okokumo.com
AUTH="Authorization: Bearer $FV_ADMIN_TOKEN"

# Moderation queue (moderation_state = pending|approved|rejected|merged|all; default pending)
curl -s -H "$AUTH" "$FV/v1/admin/ideas?moderation_state=pending"

# Approve (from pending or rejected) / reject (from pending or approved)
curl -s -X POST -H "$AUTH" "$FV/v1/admin/ideas/42/approve"
curl -s -X POST -H "$AUTH" "$FV/v1/admin/ideas/42/reject"

# Merge duplicate 42 into 17: votes move (a voter's existing vote on 17 wins; 17's author's votes are dropped)
curl -s -X POST -H "$AUTH" -H 'Content-Type: application/json' -d '{"into_id":17}' "$FV/v1/admin/ideas/42/merge"
# -> {"idea":{...17...},"moved":3,"dropped":1}

# Status: under_review | planned | in_progress | shipped | declined
curl -s -X PUT -H "$AUTH" -H 'Content-Type: application/json' -d '{"status":"planned"}' "$FV/v1/admin/ideas/17/status"

# Edit title/body (either field optional)
curl -s -X PATCH -H "$AUTH" -H 'Content-Type: application/json' -d '{"title":"Dark mode"}' "$FV/v1/admin/ideas/17"

# Create an idea as admin (approved immediately, no author)
curl -s -X POST -H "$AUTH" -H 'Content-Type: application/json' \
     -d '{"title":"Public roadmap","body":"","status":"planned"}' "$FV/v1/admin/ideas"

# Delete an idea (its votes and ideas merged into it are deleted too) -> 204
curl -s -X DELETE -H "$AUTH" "$FV/v1/admin/ideas/42"

# GDPR: erase a user -> {"votes_deleted":n,"ideas_anonymised":m}
curl -s -X DELETE -H "$AUTH" "$FV/v1/admin/users/usr_8f2c"
```

Invalid transitions (merging into a rejected idea, approving a merged one, …) return `400 invalid_input`
with an explanatory message.

**Admin page:** open `https://<fv-host>/admin`, paste `FV_ADMIN_TOKEN`. It shows the pending queue
(approve / reject / merge-into-id / edit / delete), approved ideas with a status selector, rejected and merged
lists, "add an idea" and the GDPR form. The session cookie lasts 12 h; all actions are CSRF-protected POST forms.
No JavaScript is required.

## 10. GDPR hook

When a user deletes their account, the host must call:

```
DELETE https://<fv-host>/v1/admin/users/{sub}
Authorization: Bearer $FV_ADMIN_TOKEN
```

This deletes all of the user's votes and removes them as author of their ideas (the ideas stay, anonymous).
It is idempotent — retry on failure. URL-escape `sub` if it can contain `/` or other reserved characters.
The host needs `FV_ADMIN_TOKEN` for this; keep it server-side only. FeatureVote stores no other personal data.

## 11. API reference

All JSON. `/v1/*` responses carry `Cache-Control: no-store`. Request bodies are limited to 16 KiB.

| Method & path | Auth | Success | Notes |
|---------------|------|---------|-------|
| `GET /healthz` | — | `200 {"status":"ok"}` | `503 {"status":"db_unavailable"}` when the DB ping fails |
| `GET /widget.js` | — | `200` JS | ETag / `304` |
| `GET /v1/ideas?sort=top\|new&status=&limit=50&offset=0` | — | `200 {"ideas":[Idea],"total":n}` | approved only; `limit` ≤ 100; top = score, up, newest |
| `GET /v1/ideas/{id}` | — | `200 Idea` | `404` unless approved |
| `GET /v1/me` | host token | `200 {"voter":b,"votes":[{"idea_id":1,"value":1}],"ideas":[OwnIdea]}` | works with `voter:false` |
| `GET /v1/me/votes` | host token | `200 {"votes":[...]}` | |
| `GET /v1/me/ideas` | host token | `200 {"ideas":[OwnIdea]}` | own pending + approved ideas |
| `POST /v1/ideas` `{"title","body"}` | host token, voter | `201 OwnIdea` (pending) | title 1–120 chars, body ≤ 2000 (trimmed); 5/day |
| `PUT /v1/ideas/{id}/vote` `{"value":1\|-1}` | host token, voter | `200 {"idea":Idea,"my_vote":1\|-1}` | `403 own_idea`, `409 voting_closed` |
| `DELETE /v1/ideas/{id}/vote` | host token, voter | `200 {"idea":Idea,"my_vote":0}` | idempotent |
| `GET /v1/admin/ideas?moderation_state=` | admin | `200 {"ideas":[AdminIdea]}` | |
| `POST /v1/admin/ideas` | admin | `201 AdminIdea` | |
| `POST /v1/admin/ideas/{id}/approve` · `/reject` | admin | `200 AdminIdea` | |
| `POST /v1/admin/ideas/{id}/merge` `{"into_id"}` | admin | `200 {"idea":AdminIdea,"moved":n,"dropped":m}` | |
| `PUT /v1/admin/ideas/{id}/status` `{"status"}` | admin | `200 AdminIdea` | |
| `PATCH /v1/admin/ideas/{id}` `{"title"?,"body"?}` | admin | `200 AdminIdea` | |
| `DELETE /v1/admin/ideas/{id}` | admin | `204` | |
| `DELETE /v1/admin/users/{sub}` | admin | `200 {"votes_deleted":n,"ideas_anonymised":m}` | |

Check order for writes: token (`401`) → `voter` (`403 not_eligible`) → rate limit (`429`) → input / idea checks.

Objects:

```json
// Idea (public; never contains author_sub or moderation_state)
{"id":1,"title":"Dark mode","body":"…","status":"planned","up":3,"down":1,"score":2,"created_at":"2026-09-30T12:00:00Z"}
// OwnIdea = Idea + "moderation_state": "pending" | "approved"
// AdminIdea = Idea + "author_sub" (string|null), "moderation_state", "merged_into_id" (number|null), "updated_at"
```

Errors: `{"error":{"code":"<code>","message":"<english developer message>"}}`. Map `code`, not `message`.

| HTTP | `code` | When |
|------|--------|------|
| 400 | `invalid_input` | bad JSON, field limits, unknown status/sort, invalid admin transition |
| 401 | `unauthorized` | missing bearer token (or wrong admin token) |
| 401 | `invalid_token` | host token malformed, bad signature, wrong `iss`/`alg`, lifetime > 15 min, … |
| 401 | `token_expired` | host token past `exp` + skew — fetch a new one |
| 403 | `not_eligible` | token has `voter:false` |
| 403 | `own_idea` | voting on your own idea |
| 403 | `forbidden_origin` | CORS preflight from an origin not in `FV_ALLOWED_ORIGINS` |
| 404 | `not_found` | unknown idea, or not approved |
| 409 | `voting_closed` | idea is `shipped` or `declined` |
| 413 | `payload_too_large` | body > 16 KiB |
| 429 | `rate_limited` | submit or vote limit; honour `Retry-After` (seconds) |
| 500 | `internal` | server error |
