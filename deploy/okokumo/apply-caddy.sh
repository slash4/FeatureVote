#!/usr/bin/env bash
# Put feedback.okokumo.com live behind Caddy on the okokumo prod host.
#
# Run as root ON the prod host, and only AFTER the `feedback` A record exists.
# The script refuses to touch Caddy until the record resolves to this host at
# both Scaleway authoritative servers and two public resolvers: a reload with
# the block in place makes Caddy order a certificate at once, and every failed
# ACME authorisation counts against Let's Encrypt's failed-validation limit.
#
# Idempotent: a second run with the block already present only re-verifies.
# Rollback: rollback-caddy.sh (restores the backup this script writes).
#
# DRY_RUN=1 skips the DNS gate, builds the edited file next to the live one,
# runs `caddy validate` on it, prints the diff and deletes it. No reload, no
# change to the live file.
set -euo pipefail
DRY_RUN=${DRY_RUN:-0}

HOST=feedback.okokumo.com
IP=163.172.163.71
CF=/etc/caddy/Caddyfile
MARKER='# Tower custom domains: every hostname'
SITES="okokumo.com www.okokumo.com app.okokumo.com status.okokumo.com admin.okokumo.com"

codes() {
	for h in $SITES; do
		printf '%s %s\n' "$h" "$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "https://$h/")"
	done
}

# 1. DNS gate.
for r in ns0.dom.scw.cloud ns1.dom.scw.cloud 1.1.1.1 8.8.8.8; do
	[ "$DRY_RUN" = 1 ] && { echo "DRY_RUN: DNS gate skipped"; break; }
	got=$(dig +short A "$HOST" "@$r" | tail -n1)
	if [ "$got" != "$IP" ]; then
		echo "DNS not ready at $r (got '${got:-nothing}', want $IP). Caddy untouched." >&2
		exit 1
	fi
done
[ "$DRY_RUN" = 1 ] || echo "DNS ok: $HOST -> $IP at authoritative + public resolvers"

# 2. The service must be up before Caddy points at it.
systemctl is-active --quiet featurevote || { echo "featurevote.service is not active" >&2; exit 1; }
curl -fsS --max-time 5 http://127.0.0.1:8095/healthz >/dev/null

if grep -q "^$HOST {" "$CF"; then
	echo "Block already present in $CF; skipping edit."
	[ "$DRY_RUN" = 1 ] && exit 0
else
	if [ "$DRY_RUN" != 1 ]; then
		echo "== status codes BEFORE"
		codes | tee /root/featurevote-caddy-before.txt

		backup="$CF.bak.$(date -u +%Y%m%dT%H%M%SZ)"
		cp -a "$CF" "$backup"
		echo "$backup" > /root/featurevote-caddy-backup-path
	fi

	# Insert the block right above the hostless Tower catch-all, which must
	# stay last. Same text as deploy/Caddyfile.prod on the okokumo branch
	# deploy/feature-vote.
	python3 - "$CF" "$MARKER" <<'PY'
import sys
path, marker = sys.argv[1], sys.argv[2]
src = open(path).read()
if src.count(marker) != 1:
    sys.exit(f"marker found {src.count(marker)} times, want 1; not editing")
block = (
    "# FeatureVote (github.com/slash4/FeatureVote): the public idea board, the\n"
    "# embeddable /widget.js and its own /admin moderation page. A separate Go\n"
    "# service (featurevote.service, 127.0.0.1:8095, own Postgres DB) installed by\n"
    "# hand, not by this repo's deploy — see FeatureVote docs/DEPLOY-okokumo.md.\n"
    "# The service sets its own CORS, nosniff and admin-page CSP/X-Frame-Options,\n"
    "# so nothing is added here beyond what the sibling blocks do.\n"
    "feedback.okokumo.com {\n"
    "\tencode zstd gzip\n"
    "\treverse_proxy localhost:8095\n"
    "}\n"
    "\n"
)
open(path + ".new", "w").write(src.replace(marker, block + marker, 1))
PY

	if ! caddy validate --config "$CF.new" --adapter caddyfile; then
		rm -f "$CF.new"
		echo "caddy validate failed; live file untouched" >&2
		exit 1
	fi
	if [ "$DRY_RUN" = 1 ]; then
		diff -u "$CF" "$CF.new" || true
		rm -f "$CF.new"
		echo "DRY_RUN: validate ok; live file untouched, no reload."
		exit 0
	fi
	chmod --reference="$CF" "$CF.new"
	mv "$CF.new" "$CF"
	systemctl reload caddy
	echo "Caddy reloaded."
fi

# 3. Wait for the certificate (first request after reload triggers issuance).
for _ in $(seq 1 24); do
	if curl -fsS --max-time 5 "https://$HOST/healthz" >/dev/null 2>&1; then
		break
	fi
	sleep 5
done

echo "== https://$HOST/healthz"
curl -sD- --max-time 10 "https://$HOST/healthz"; echo
echo "== https://$HOST/v1/ideas"
curl -s --max-time 10 "https://$HOST/v1/ideas"; echo
echo "== https://$HOST/widget.js"
curl -sI --max-time 10 "https://$HOST/widget.js" | grep -i -E '^HTTP|^content-type'
echo "== https://$HOST/admin"
curl -s --max-time 10 "https://$HOST/admin" | grep -o -i -E '<title>[^<]*</title>|type="password"' | head -2

echo "== status codes AFTER"
codes | tee /root/featurevote-caddy-after.txt
if [ -f /root/featurevote-caddy-before.txt ] && ! diff /root/featurevote-caddy-before.txt /root/featurevote-caddy-after.txt; then
	echo "!! an existing site changed status code; consider rollback-caddy.sh" >&2
	exit 2
fi
echo "Existing sites unchanged."
