#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if ! command -v k6 >/dev/null 2>&1; then
  echo "k6 is required. Install it with: brew install k6" >&2
  exit 1
fi

RUN_ID="${RUN_ID:-$(date +%Y%m%d%H%M%S)}"
ORG_ID="${ORG_ID:-org_sim_${RUN_ID}}"
AUTHSTUB_ADDRESS="${AUTHSTUB_ADDRESS:-127.0.0.1:18080}"
# Teams, files and email run in one oryxa process, so teams and files share
# its address.
ORYXA_ADDRESS="${ORYXA_ADDRESS:-127.0.0.1:18083}"
DATABASE_URL="${DATABASE_URL:-postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable}"
AWS_ENDPOINT_URL="${AWS_ENDPOINT_URL:-http://localhost:4566}"
AUTH_URL="http://${AUTHSTUB_ADDRESS}"
TEAMS_URL="http://${ORYXA_ADDRESS}"
FILES_URL="http://${ORYXA_ADDRESS}"

MAILPIT_URL="${MAILPIT_URL:-http://127.0.0.1:8025}"
SES_ENDPOINT="${AWS_ENDPOINT_URL_SESV2:-http://localhost:4567}"
EMAIL_SMTP_ADDR="${EMAIL_SMTP_ADDR:-127.0.0.1:1025}"

echo "starting local postgres, LocalStack, SES, and Mailpit"
docker compose up -d postgres eventbridge ses mailpit >/dev/null

echo "waiting for postgres, LocalStack, SES, and Mailpit"
for _ in $(seq 1 40); do
  if docker compose exec -T postgres pg_isready -U oryxa -d oryxa >/dev/null 2>&1 \
    && curl -sf "${AWS_ENDPOINT_URL}/_localstack/health" >/dev/null 2>&1 \
    && curl -sf "${SES_ENDPOINT}/moto-api/" >/dev/null 2>&1 \
    && curl -sf "${MAILPIT_URL}/api/v1/info" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

STACK_LOG="$(mktemp)"
cleanup() {
  if [[ -n "${STACK_PID:-}" ]]; then
    kill "${STACK_PID}" >/dev/null 2>&1 || true
    wait "${STACK_PID}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

STACK_BIN="$(mktemp -t oryxa-stack)"
echo "building stack harness"
go build -o "${STACK_BIN}" ./test/cmd/stack

echo "starting stack harness (log: ${STACK_LOG})"
env \
  ORG_ID="${ORG_ID}" \
  DATABASE_URL="${DATABASE_URL}" \
  AWS_ENDPOINT_URL="${AWS_ENDPOINT_URL}" \
  AUTHSTUB_ADDRESS="${AUTHSTUB_ADDRESS}" \
  ORYXA_ADDRESS="${ORYXA_ADDRESS}" \
  AWS_ENDPOINT_URL_SESV2="${SES_ENDPOINT}" \
  EMAIL_SMTP_ADDR="${EMAIL_SMTP_ADDR}" \
  AWS_REGION=ap-south-1 \
  AWS_ACCESS_KEY_ID=test \
  AWS_SECRET_ACCESS_KEY=test \
  "${STACK_BIN}" >"${STACK_LOG}" 2>&1 &
STACK_PID=$!

echo "waiting for the auth stub and oryxa"
ready=0
for _ in $(seq 1 60); do
  if ! kill -0 "${STACK_PID}" >/dev/null 2>&1; then
    echo "stack harness exited before it was ready" >&2
    cat "${STACK_LOG}" >&2
    exit 1
  fi
  if curl -sf -o /dev/null "${AUTH_URL}/healthz" \
    && curl -sf -o /dev/null "http://${ORYXA_ADDRESS}/healthz"; then
    ready=1
    break
  fi
  sleep 0.5
done
if [[ "${ready}" -ne 1 ]]; then
  echo "stack harness did not become ready" >&2
  cat "${STACK_LOG}" >&2
  exit 1
fi

echo "running k6 against org ${ORG_ID}"
k6 run \
  -e ORG_ID="${ORG_ID}" \
  -e RUN_ID="${RUN_ID}" \
  -e AUTH_URL="${AUTH_URL}" \
  -e TEAMS_URL="${TEAMS_URL}" \
  -e FILES_URL="${FILES_URL}" \
  -e MAILPIT_URL="${MAILPIT_URL}" \
  "${ROOT}/test/k6/stack.js"

echo
echo "Mailpit: ${MAILPIT_URL}"
