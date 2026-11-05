# Moderation-queue watchdog

Every submitted suggestion starts `pending` and stays invisible until an admin moderates it. Nothing in the
service tells anyone that the queue is filling up, so `fvpending` runs once a day (07:00 UTC, systemd timer)
and alerts through an Okokumo cron check when any suggestion has been pending longer than 48h.

It only needs `FV_DATABASE_URL` (read-only queries), so it works for any FeatureVote deployment
(Okokumo, DoLoop, ...): give each deployment its own check and env file.

How it reports, with `FV_MODERATION_PING_URL` set (healthchecks.io-compatible ping protocol):

| Outcome                                       | Exit | Ping                                                    |
|-----------------------------------------------|------|---------------------------------------------------------|
| nothing pending longer than `-older-than`     | 0    | `POST <url>` (report as body)                           |
| stale suggestions found                       | 1    | `POST <url>/fail`, body = the report                    |
| check could not run (DB down, bad config)     | 2    | `POST <url>/fail`, body `fvpending: check could not run: ...` |
| the ping itself failed (3 attempts)           | 2    | — (error on stderr; the check goes down when the ping is missed) |

An invalid `FV_MODERATION_PING_URL` (not absolute http(s), has a query/fragment, ends in `/fail` or
`/start`) exits 2 without pinging; the check then goes down on its own because no ping arrives. The check
alerts once on the up→down transition and recovers on the next success ping, i.e. on the first day the queue
is clean again. The unit treats exit 1 as success (`SuccessExitStatus=1`, it was already reported); exit 2
marks the unit failed.

Report (stdout and `/fail` body):

```
2 suggestions waiting for moderation longer than 48h:
  #1  pending 16d 2h  (submitted 2026-10-19 08:37 UTC)
  #9  pending 3d 1h  (submitted 2026-11-01 10:00 UTC)
Moderate at https://feedback.okokumo.com/admin
```

**Privacy:** the output goes to third-party alert channels, so it holds idea ids and ages only — never titles,
bodies or author ids. The ping URL is a secret: it is read only from the environment and never printed
(errors show scheme+host only).

## Install

1. In Okokumo create a cron check "`<product>` FeatureVote — suggestions pending moderation >48h", schedule
   `0 7 * * *`, timezone UTC, grace 1h; copy its ping URL.
2. Build and install the binary (it is also in the Docker image as `/fvpending`):
   ```sh
   CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o fvpending ./cmd/fvpending
   sudo install -o root -g root -m 0755 fvpending /opt/featurevote/fvpending
   ```
3. Env file (`FV_DATABASE_URL` comes from the service's existing `/etc/featurevote/env`). Install it
   `root:root 0600`: systemd reads `EnvironmentFile=` as root before dropping to `User=featurevote`, so the
   service user never needs to read it, and a compromised service cannot rewrite the ping URL:
   ```sh
   sudo install -o root -g root -m 0600 deploy/moderation-check/moderation-check.env.example \
     /etc/featurevote/moderation-check.env
   sudoedit /etc/featurevote/moderation-check.env     # paste the real ping URL
   ```
4. Units:
   ```sh
   sudo install -m 0644 deploy/moderation-check/featurevote-moderation-check.{service,timer} /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now featurevote-moderation-check.timer
   ```
5. Run it once now and read the result (the check should turn up or down accordingly):
   ```sh
   sudo systemctl start featurevote-moderation-check.service
   journalctl -u featurevote-moderation-check.service -o cat -n 20
   systemctl list-timers featurevote-moderation-check.timer
   ```

## Testing the /fail path without touching prod data

The binary only reads. Run it by hand as the service user with a tiny threshold, which lists every pending
idea (exit 1 and a `/fail` ping if there is at least one; with an empty queue it reports OK):

```sh
sudo systemd-run --quiet --wait --pipe --uid=featurevote --gid=featurevote \
  -p EnvironmentFile=/etc/featurevote/env -p EnvironmentFile=/etc/featurevote/moderation-check.env \
  /opt/featurevote/fvpending -older-than 1s; echo "exit $?"
```

Use `systemd-run` (or the unit) rather than sourcing the env files in a shell: systemd's `EnvironmentFile`
syntax is not shell syntax, and okokumo's `/etc/featurevote/env` has an unquoted `FV_ALLOWED_ORIGINS` list
with spaces that `.`/`source` misparses.

The check goes down and alerts; the next normal run (or `systemctl start featurevote-moderation-check.service`)
recovers it if nothing is really stale. Leave `FV_MODERATION_PING_URL` out of the environment to try it
without pinging at all.

## Rollback

```sh
sudo systemctl disable --now featurevote-moderation-check.timer
```

Then pause (or delete) the Okokumo check, otherwise its dead-man's switch alerts when the daily ping stops.
Optionally remove the units, `/etc/featurevote/moderation-check.env` and `/opt/featurevote/fvpending`.
