#!/usr/bin/env bash
# Runs the Go test suite against a real Postgres.
#
#   FV_TEST_DATABASE_URL=postgres://... scripts/test.sh [go test args]
#   FV_TEST_DATABASE_URL_FILE=/path/to/file-containing-url scripts/test.sh
#
# The database is migrated and TRUNCATEd by the tests: never point this at a
# database holding data you care about. Packages run sequentially (-p 1)
# because they share the database.
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ -z "${FV_TEST_DATABASE_URL:-}" && -n "${FV_TEST_DATABASE_URL_FILE:-}" ]]; then
  FV_TEST_DATABASE_URL="$(<"$FV_TEST_DATABASE_URL_FILE")"
fi
if [[ -z "${FV_TEST_DATABASE_URL:-}" ]]; then
  echo "FV_TEST_DATABASE_URL (or FV_TEST_DATABASE_URL_FILE) must be set; DB-backed tests would be skipped." >&2
  exit 2
fi
export FV_TEST_DATABASE_URL

exec go test -p 1 -count=1 "$@" ./...
