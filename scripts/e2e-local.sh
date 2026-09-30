#!/usr/bin/env bash
# Builds the service, starts it on a free port against $FV_DATABASE_URL with
# freshly generated secrets, waits for /healthz, runs scripts/e2e.sh and
# stops the service.
#
#   FV_DATABASE_URL=postgres://... scripts/e2e-local.sh
#   FV_DATABASE_URL_FILE=/path/to/file-containing-url scripts/e2e-local.sh
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ -z "${FV_DATABASE_URL:-}" && -n "${FV_DATABASE_URL_FILE:-}" ]]; then
  FV_DATABASE_URL="$(<"$FV_DATABASE_URL_FILE")"
fi
: "${FV_DATABASE_URL:?FV_DATABASE_URL (or FV_DATABASE_URL_FILE) must be set}"

BIN="$(mktemp -d)"
PID=""
stop() {
  if [[ -n "$PID" ]] && kill -0 "$PID" 2>/dev/null; then
    kill -TERM "$PID"
    wait "$PID" || true
  fi
  rm -f "$BIN"/featurevote "$BIN"/fvtoken "$BIN"/service.log && rmdir "$BIN"
}
trap stop EXIT

echo "building..."
go build -o "$BIN/featurevote" ./cmd/featurevote
go build -o "$BIN/fvtoken" ./cmd/fvtoken

PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
export FV_DATABASE_URL
export FV_HOST_SECRET="e2e-host-$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
export FV_ADMIN_TOKEN="e2e-admin-$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
export FV_HOST_ISSUER="e2e"
export FV_LISTEN_ADDR="127.0.0.1:$PORT"
export FV_API="http://127.0.0.1:$PORT"
export FV_TOKEN_BIN="$BIN/fvtoken"

"$BIN/featurevote" >"$BIN/service.log" 2>&1 &
PID=$!

for _ in $(seq 1 50); do
  if curl -fs "$FV_API/healthz" >/dev/null 2>&1; then break; fi
  if ! kill -0 "$PID" 2>/dev/null; then echo "service exited:" >&2; cat "$BIN/service.log" >&2; exit 1; fi
  sleep 0.2
done
curl -fs "$FV_API/healthz" >/dev/null || { echo "service not healthy:" >&2; cat "$BIN/service.log" >&2; exit 1; }
echo "service up on $FV_API (pid $PID)"

if ! scripts/e2e.sh; then
  echo "--- service log ---" >&2; cat "$BIN/service.log" >&2
  exit 1
fi
