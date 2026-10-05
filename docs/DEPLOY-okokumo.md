# FeatureVote for okokumo — production install

Instance: https://feedback.okokumo.com, on the okokumo prod host
(163.172.163.71, `sentinel-prod-app`). Installed by hand on 2026-10-05 for
kanban card t_8d857924; not managed by the okokumo deploy workflows.

## What is where

| Thing | Value |
|---|---|
| Shipped sha | `f543065377a65d29ae3568fb5c3311dee1a399de` (in `/opt/featurevote/VERSION`; also `vcs.revision` in the binary, `vcs.modified=false`) |
| Binaries | `/opt/featurevote/featurevote`, `/opt/featurevote/fvtoken` (root-owned, 0755) |
| Service | `featurevote.service`, user/group `featurevote` (system user, no shell use) |
| Listen | `127.0.0.1:8095` (loopback only; Caddy terminates TLS) |
| Env file | `/etc/featurevote/env`, 0600 `featurevote:featurevote`, dir 0750 `root:featurevote` |
| Database | `featurevote` DB owned by role `featurevote` (no superuser, no createdb) in the existing `sentinel-postgres` container (TimescaleDB pg16). The role reads 0 of the okokumo `sentinel` DB's 26 tables. |
| Admin token | `/root/featurevote-admin-token`, 0400 root (copy of `FV_ADMIN_TOKEN`) |
| Host secret | `FV_HOST_SECRET` in the env file; also GitHub `slash4/Okokumo`, environment `prod`, secret `FEATURE_VOTE_SECRET` |
| Caddy | `feedback.okokumo.com` block in `/etc/caddy/Caddyfile`; the same block is in okokumo `deploy/Caddyfile.prod` (branch `deploy/feature-vote`) |

Env keys: `FV_DATABASE_URL`, `FV_HOST_SECRET`, `FV_ADMIN_TOKEN`
(both `openssl rand -hex 32`, generated on the host), `FV_HOST_ISSUER=okokumo`,
`FV_ALLOWED_ORIGINS=https://app.okokumo.com, https://okokumo.com, https://www.okokumo.com`,
`FV_LISTEN_ADDR=127.0.0.1:8095`. `FV_AUDIENCE` is deliberately unset: the
okokumo minter (`backend/internal/featurevote`) sends no `aud` claim.

## Migrations

Run automatically at boot by the service itself (advisory-locked, one
transaction per file, recorded in `schema_migrations`). An upgrade with a new
migration applies it on the restart; watch `journalctl -u featurevote` for
`migration applied`.

## Unit hardening

Stricter than the host's `sentinel-*` units: `NoNewPrivileges`, empty
capability set, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`,
`PrivateDevices`, kernel/cgroup/clock protection, `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX`,
`SystemCallFilter=@system-service ~@privileged`, `MemoryDenyWriteExecute`,
`MemoryMax=256M`, `TasksMax=256`, `UMask=0077`. See `systemctl cat featurevote`.

## Upgrade

```sh
# on a dev machine, in this repo, at the sha to ship (clean tree)
git checkout <sha> && CGO_ENABLED=0 go build -o featurevote ./cmd/featurevote \
  && CGO_ENABLED=0 go build -o fvtoken ./cmd/fvtoken
# copy both to the host, then as root on the host:
install -m 0755 featurevote fvtoken /opt/featurevote/ && echo <sha> > /opt/featurevote/VERSION
systemctl restart featurevote && journalctl -u featurevote -n 20 --no-pager
curl -s https://feedback.okokumo.com/healthz
```

## Backups

The host has **no Postgres backup routine** (no cron, no timer, no dump script
as of 2026-10-05) — for the okokumo `sentinel` DB either. Nothing was added to
an existing routine because there is none. Data dir is a bind mount at
`/var/lib/sentinel-postgres`; whether Scaleway volume snapshots cover it is not
verified from the host.

## Rollback

`/root/featurevote-rollback-caddy.sh` (this repo: `deploy/okokumo/rollback-caddy.sh`)
restores the pre-change Caddyfile and reloads; `--stop` also stops and
disables the unit. The okokumo app does not depend on the service until
`FEATURE_VOTE_*` ship in a prod tag.

## Caveat: the live Caddyfile is overwritten by okokumo prod deploys

`deploy-prod.yml` runs `setup-host.sh` with `CADDYFILE_NAME=Caddyfile.prod`,
which replaces `/etc/caddy/Caddyfile`. Until okokumo branch
`deploy/feature-vote` is merged, the next okokumo prod tag removes the
`feedback` block (the service keeps running; the hostname stops serving).
