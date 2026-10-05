#!/usr/bin/env bash
# Undo apply-caddy.sh on the okokumo prod host (run as root ON the host).
# Restores the Caddyfile backup it wrote, validates, reloads. Pass --stop to
# also stop and disable featurevote.service. Nothing in the okokumo app
# depends on the service until FEATURE_VOTE_* ship in a prod tag.
set -euo pipefail
CF=/etc/caddy/Caddyfile
backup=$(cat /root/featurevote-caddy-backup-path)
caddy validate --config "$backup" --adapter caddyfile
cp -a "$CF" "$CF.rolledback.$(date -u +%Y%m%dT%H%M%SZ)"
cp -a "$backup" "$CF"
systemctl reload caddy
echo "Caddy restored from $backup and reloaded."
if [ "${1:-}" = "--stop" ]; then
	systemctl disable --now featurevote
	echo "featurevote.service stopped and disabled."
fi
