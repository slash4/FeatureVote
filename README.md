# FeatureVote

A small, reusable feature-voting service: a Go HTTP service + Postgres + an embeddable floating widget
(served by the service at `/widget.js`). It is deployed **once per host product** (Okokumo, DoLoop), each
with its own database, domain and config.

- Users submit ideas; every idea starts **pending** and is public only after an admin approves it.
- One vote per (idea, user), `+1` or `-1`, changeable and removable; no voting on your own ideas.
- The host decides who may vote and passes an opaque user id in a short-lived HS256 JWT. FeatureVote
  stores no emails or names; authors are anonymous publicly.

**Integrating a product? Read [docs/INTEGRATION.md](docs/INTEGRATION.md)** — the host contract.

## Layout

```
cmd/featurevote   service           cmd/fvtoken   mint/verify host tokens     cmd/demohost  demo host app
internal/config   env config        internal/hosttoken  JWT verify/mint      internal/store  Postgres queries
internal/migrate  migration runner  internal/api  HTTP handlers + admin page  internal/web   embedded widget.js
migrations/       SQL migrations    widget/       TypeScript widget source    examples/      demo page
```

## Run locally

With Docker:

```sh
docker compose up --build
open http://localhost:8090/api/login?as=alice\&voter=true   # demo host, logged in as "alice"
open http://localhost:8080/admin                             # admin page (token in docker-compose.yml)
```

Without Docker (Go 1.26, Node 22, a Postgres 16):

```sh
npm --prefix widget ci && npm --prefix widget run build   # -> internal/web/static/widget.js
export FV_DATABASE_URL=postgres://...  FV_HOST_ISSUER=demo \
       FV_HOST_SECRET=$(openssl rand -hex 32) FV_ADMIN_TOKEN=$(openssl rand -hex 32) \
       FV_ALLOWED_ORIGINS=http://localhost:8090 FV_COOKIE_SECURE=false
go run ./cmd/featurevote &
go run ./cmd/demohost          # http://localhost:8090/api/login?as=alice&voter=true
```

Mint a token by hand: `go run ./cmd/fvtoken -sub alice -voter` (verify one: `-verify <jwt>`).

## Test

Tests run against a real Postgres (no mocks); the database is migrated and truncated, so use a
dedicated one:

```sh
FV_TEST_DATABASE_URL=postgres://.../featurevote_test scripts/test.sh        # go test -p 1 -count=1 ./...
npm --prefix widget test                                                     # widget unit tests
ruby scripts/verify-ruby-mint.rb                                             # doc's Ruby minting example
```

## End-to-end

`scripts/e2e.sh` drives a running service with curl + jq (submit → moderate → vote/switch/remove → status →
GDPR delete → cleanup). `scripts/e2e-local.sh` builds the binary, starts it on a free port with generated
secrets against `$FV_DATABASE_URL`, runs the e2e script and stops the service:

```sh
FV_DATABASE_URL=postgres://.../featurevote_dev scripts/e2e-local.sh
```

## Design note: scores are computed, not stored

An idea's `up`, `down` and `score` are computed at read time (`COUNT(*) FILTER (...)` over `votes`, per
idea), not denormalised onto `ideas`. Boards are small (hundreds of ideas, thousands of votes), a computed
score cannot drift, and merge / GDPR-delete / vote-switch recounts are correct by construction with no
triggers. Revisit only if list latency matters — then add a counter cache maintained in the same transaction
as the vote write.

## Operational notes

- Migrations run automatically at boot (advisory-locked, one transaction per file).
- `GET /healthz` pings the database (use it as the liveness/readiness probe; the distroless image has no
  `HEALTHCHECK`).
- The vote rate limiter is in memory, per process — fine because each product runs one instance. The idea
  submission limit is counted in the database.
- Logs are JSON (`log/slog`), one line per request; tokens and bodies are never logged.

## License

FeatureVote is free software, licensed under the **GNU Affero General Public License v3.0 or later**
([LICENSE](LICENSE)). If you run a modified version as a network service, the AGPL requires you to offer
its users the corresponding source code.
