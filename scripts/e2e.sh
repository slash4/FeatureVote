#!/usr/bin/env bash
# End-to-end check against a RUNNING FeatureVote service.
#
#   FV_API=http://localhost:8080 FV_HOST_SECRET=... FV_HOST_ISSUER=... FV_ADMIN_TOKEN=... scripts/e2e.sh
#
# Needs curl and jq. Tokens are minted with cmd/fvtoken (built once from
# source, or FV_TOKEN_BIN=/path/to/fvtoken). Every run uses unique user subs
# and a unique title, so it is safe to repeat against the same database, and
# it deletes the idea it created.
set -euo pipefail
cd "$(dirname "$0")/.."

API="${FV_API:-http://localhost:8080}"
: "${FV_HOST_SECRET:?FV_HOST_SECRET must be set}"
: "${FV_HOST_ISSUER:?FV_HOST_ISSUER must be set}"
: "${FV_ADMIN_TOKEN:?FV_ADMIN_TOKEN must be set}"
command -v curl >/dev/null || { echo "curl is required" >&2; exit 1; }
command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }

WORK="$(mktemp -d)"
IDEA_ID=""
cleanup() {
  if [[ -n "$IDEA_ID" ]]; then
    curl -s -o /dev/null -X DELETE -H "Authorization: Bearer $FV_ADMIN_TOKEN" "$API/v1/admin/ideas/$IDEA_ID" || true
  fi
  rm -f "$WORK"/* && rmdir "$WORK"
}
trap cleanup EXIT

if [[ -n "${FV_TOKEN_BIN:-}" ]]; then
  FVTOKEN="$FV_TOKEN_BIN"
else
  FVTOKEN="$WORK/fvtoken"
  go build -o "$FVTOKEN" ./cmd/fvtoken
fi

RUN="$(date +%s)-$$"
ALICE="e2e-alice-$RUN" BOB="e2e-bob-$RUN" CAROL="e2e-carol-$RUN" DAVE="e2e-dave-$RUN"
TITLE="E2E idea $RUN"

mint() { "$FVTOKEN" -sub "$1" -voter="$2" -ttl "${3:-10m}"; }
T_ALICE="$(mint "$ALICE" true)"
T_BOB="$(mint "$BOB" true)"
T_CAROL="$(mint "$CAROL" false)"
T_DAVE="$(mint "$DAVE" true)"
T_EXPIRED="$(mint "$BOB" true -5m)"

fail() { echo "✗ $*" >&2; echo "  last response ($STATUS): $(cat "$WORK/body" 2>/dev/null)" >&2; exit 1; }
ok() { echo "✓ $*"; }

# req METHOD PATH TOKEN [JSON_BODY] -> sets STATUS, response body in $WORK/body
STATUS=""
req() {
  local method="$1" path="$2" token="$3" body="${4:-}"
  local args=(-s -o "$WORK/body" -w '%{http_code}' -X "$method")
  [[ -n "$token" ]] && args+=(-H "Authorization: Bearer $token")
  [[ -n "$body" ]] && args+=(-H 'Content-Type: application/json' --data "$body")
  STATUS="$(curl "${args[@]}" "$API$path")"
}
expect_status() { [[ "$STATUS" == "$1" ]] || fail "$2: expected HTTP $1, got $STATUS"; }
expect_jq() { jq -e "$1" "$WORK/body" >/dev/null || fail "$2: assertion failed: $1"; }
expect_code() { expect_status "$1" "$3"; expect_jq ".error.code == \"$2\"" "$3"; }
admin() { req "$1" "$2" "$FV_ADMIN_TOKEN" "${3:-}"; }
counts() { # counts UP DOWN SCORE STEP
  req GET "/v1/ideas/$IDEA_ID" ""
  expect_status 200 "$4"
  expect_jq ".up == $1 and .down == $2 and .score == $3" "$4"
}

req GET /healthz ""
expect_status 200 healthz; expect_jq '.status == "ok"' healthz
ok "healthz"

req POST /v1/ideas "$T_ALICE" "$(jq -nc --arg t "$TITLE" '{title: $t, body: "Created by scripts/e2e.sh"}')"
expect_status 201 "submit idea"
expect_jq '.moderation_state == "pending" and .up == 0' "submit idea"
IDEA_ID="$(jq -r .id "$WORK/body")"
ok "alice submits idea #$IDEA_ID -> 201 pending"

req GET "/v1/ideas?sort=new&limit=100" ""
expect_status 200 "public list"; expect_jq "all(.ideas[]; .id != $IDEA_ID)" "pending hidden from list"
ok "public list does not contain the pending idea"

req GET "/v1/ideas/$IDEA_ID" ""
expect_code 404 not_found "pending hidden by id"
ok "public GET of pending idea -> 404"

req GET /v1/me "$T_ALICE"
expect_status 200 "alice /v1/me"
expect_jq ".voter == true and any(.ideas[]; .id == $IDEA_ID and .moderation_state == \"pending\")" "alice /v1/me"
ok "alice's /v1/me shows the idea as pending"

admin POST "/v1/admin/ideas/$IDEA_ID/approve"
expect_status 200 approve; expect_jq '.moderation_state == "approved"' approve
ok "admin approves"

req GET "/v1/ideas?sort=new&limit=100" ""
expect_status 200 "public list"
expect_jq "any(.ideas[]; .id == $IDEA_ID and .up == 0 and .down == 0 and .status == \"under_review\")" "approved idea listed"
expect_jq 'all(.ideas[]; has("author_sub") | not)' "no author_sub in public list"
ok "public list contains it with up=0 down=0"

req PUT "/v1/ideas/$IDEA_ID/vote" "$T_ALICE" '{"value":1}'
expect_code 403 own_idea "self-vote"
ok "alice self-vote -> 403 own_idea"

req PUT "/v1/ideas/$IDEA_ID/vote" "$T_BOB" '{"value":1}'
expect_status 200 "bob upvote"; expect_jq '.my_vote == 1 and .idea.up == 1 and .idea.score == 1' "bob upvote"
ok "bob upvotes -> up=1 score=1"

req PUT "/v1/ideas/$IDEA_ID/vote" "$T_BOB" '{"value":1}'
expect_status 200 "bob upvote again"
counts 1 0 1 "no stacking"
ok "bob upvotes again -> still up=1 (no stacking)"

req PUT "/v1/ideas/$IDEA_ID/vote" "$T_BOB" '{"value":-1}'
expect_status 200 "bob switches"; expect_jq '.my_vote == -1' "bob switches"
counts 0 1 -1 "bob switches"
ok "bob switches to down -> up=0 down=1 score=-1"

req PUT "/v1/ideas/$IDEA_ID/vote" "$T_CAROL" '{"value":1}'
expect_code 403 not_eligible "carol vote"
ok "carol (voter=false) vote -> 403 not_eligible"

req PUT "/v1/ideas/$IDEA_ID/vote" "$T_DAVE" '{"value":1}'
expect_status 200 "dave upvote"
counts 1 1 0 "dave upvote"
ok "dave upvotes -> up=1 down=1 score=0"

req DELETE "/v1/ideas/$IDEA_ID/vote" "$T_BOB"
expect_status 200 "bob removes"; expect_jq '.my_vote == 0' "bob removes"
counts 1 0 1 "bob removes"
ok "bob removes vote -> up=1 down=0 score=1"

req GET /v1/me "$T_EXPIRED"
expect_code 401 token_expired "expired token"
ok "expired token -> 401 token_expired"

admin PUT "/v1/admin/ideas/$IDEA_ID/status" '{"status":"planned"}'
expect_status 200 "set status"
req GET "/v1/ideas/$IDEA_ID" ""
expect_status 200 "public status"; expect_jq '.status == "planned"' "public status"
ok "admin sets status planned -> public shows planned"

admin DELETE "/v1/admin/users/$DAVE"
expect_status 200 "gdpr delete"; expect_jq '.votes_deleted == 1' "gdpr delete"
counts 0 0 0 "after gdpr delete"
ok "GDPR delete dave -> score 0"

admin DELETE "/v1/admin/ideas/$IDEA_ID"
expect_status 204 "cleanup delete"
req GET "/v1/ideas/$IDEA_ID" ""
expect_code 404 not_found "deleted idea"
IDEA_ID=""
ok "cleanup: admin deletes idea"

echo "All e2e checks passed against $API"
