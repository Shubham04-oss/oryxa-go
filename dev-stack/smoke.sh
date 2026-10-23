#!/usr/bin/env bash
# Smoke-test a running dev stack as the stub's member: list agents, get an
# agent's reply (needs gcloud ADC), ask the outsider's agent, and index and
# search a corpus. Exits non-zero at the first step that fails.
set -euo pipefail
. "$(dirname "$0")/env.sh"
# Through the stub's front door, as web-next goes.
BASE=http://localhost:$AUTHSTUB_PORT/v1/organizations/org_sim
fail() { echo "FAIL: $*" >&2; exit 1; }
call() { # role method path [body]
  curl -sS --fail-with-body -X "$2" -H "Authorization: Bearer $1" -H 'Content-Type: application/json' \
    ${4:+--data "$4"} "$BASE$3"
}
primary() { call "$1" GET /agents | jq -r '.agents[] | select(.kind == "primary") | .id'; }

agent=$(primary member) || fail "list agents"
[ -n "$agent" ] || fail "member has no primary agent"
echo "ok   member's agents listed (primary $agent)"

stream=$(curl -sS -N --max-time 180 -X POST -H 'Authorization: Bearer member' -H 'Content-Type: application/json' \
  --data '{"message":"Reply with just the word pong."}' "$BASE/agents/$agent/runs") || fail "start a thread"
reply=$(sed -n 's/^data: //p' <<<"$stream" | jq -rs '[.[] | select(.type == "message" and .author != "member")] | last | .text // empty')
grep -q '"type":"run_completed"' <<<"$stream" && [ -n "$reply" ] || fail "no agent reply: $(tail -c 600 <<<"$stream")"
echo "ok   thread started, agent replied: ${reply:0:80}"

target=$(primary outsider) || fail "list the outsider's agents"
request=$(call member POST /agent-requests "$(jq -nc --arg t "$target" \
  '{target_agent_id: $t, purpose: "dev-stack smoke test", task: "Say hello back."}')") || fail "create a request: $request"
echo "ok   request to outsider created ($(jq -r '.id // .request.id' <<<"$request"))"

corpus=$(call member POST /corpora "$(jq -nc --arg s "smoke-$(date +%s)" '{slug: $s, name: $s, scope: "member"}')" | jq -r .id) \
  || fail "create a corpus"
document=$(call member POST "/corpora/$corpus/documents" \
  '{"title":"Harbour notes","content_type":"text/plain","text":"The lighthouse keeper on Pelican Rock feeds the gulls sardines every Tuesday."}' | jq -r .id) \
  || fail "add a document"
for _ in $(seq 60); do
  status=$(call member GET "/corpora/$corpus/documents/$document" | jq -r .status)
  [ "$status" = ready ] && break
  [ "$status" = failed ] && fail "document failed to index"
  sleep 2
done
[ "$status" = ready ] || fail "document still $status after 2 minutes"
matches=$(call member POST "/corpora/$corpus/search" '{"query":"what do the gulls eat?","top_k":3}' | jq '.matches | length') \
  || fail "search"
[ "$matches" -gt 0 ] || fail "search found nothing"
echo "ok   corpus $corpus indexed with Vertex embeddings and searched ($matches matches)"
echo "PASS"
