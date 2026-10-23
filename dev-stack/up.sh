#!/usr/bin/env bash
# Run Oryxa with no AWS: LocalStack and Moto for AWS, the auth stub for
# Cognito, Vertex (gcloud ADC) for Gemini and embeddings. See README.md.
set -euo pipefail
. "$(dirname "$0")/env.sh"
export PATH=$PATH:/usr/local/go/bin
aws() { command aws --region "$AWS_REGION" --endpoint-url "$AWS_ENDPOINT_URL" "$@"; }

echo "compose: postgres, LocalStack, Moto SES, Mailpit ($COMPOSE_PROJECT_NAME)"
docker compose up -d --wait postgres eventbridge ses mailpit >/dev/null

# LocalStack keeps nothing across a restart, so provision again once the
# queues are gone. Cognito is the stub and Composio is not here; with no
# RAG_VECTOR_BUCKET_NAME, rag's makes only its queue.
if ! aws sqs get-queue-url --queue-name teams-events >/dev/null 2>&1; then
  echo "LocalStack: provisioning buses, queues, tables and buckets"
  for main in $(git ls-files '*/cmd/provision/main.go' | grep -vE 'cognito|composio'); do
    go run "./$(dirname "$main")" >"$RUN/provision.log" 2>&1 || { cat "$RUN/provision.log"; echo "provision failed: $main" >&2; exit 1; }
  done
fi

for name in $DEV_SIGNING_KEYS $DEV_ENCRYPTION_KEYS; do
  aws kms describe-key --key-id "alias/oryxa-dev-$name" >/dev/null 2>&1 && continue
  if [[ " $DEV_SIGNING_KEYS " == *" $name "* ]]; then
    spec=(--key-usage SIGN_VERIFY --key-spec ECC_NIST_P256)
  else
    spec=(--key-usage ENCRYPT_DECRYPT --key-spec SYMMETRIC_DEFAULT)
  fi
  key=$(aws kms create-key "${spec[@]}" --description "oryxa dev $name" --query KeyMetadata.KeyId --output text)
  aws kms create-alias --alias-name "alias/oryxa-dev-$name" --target-key-id "$key"
  echo "KMS: alias/oryxa-dev-$name"
done

docker compose exec -T postgres psql -q -U oryxa -d oryxa -v ON_ERROR_STOP=1 <dev-stack/seed.sql

go build -o "$RUN/oryxa" ./cmd/oryxa
go build -o "$RUN/authstub" ./test/cmd/authstub
dev-stack/down.sh quiet
nohup "$RUN/authstub" >"$RUN/authstub.log" 2>&1 & echo $! >"$RUN/authstub.pid"
nohup "$RUN/oryxa" serve >"$RUN/oryxa.log" 2>&1 & echo $! >"$RUN/oryxa.pid"

for _ in $(seq 60); do
  curl -sf -o /dev/null "localhost:$API_PORT/healthz" && break
  kill -0 "$(cat "$RUN/oryxa.pid")" 2>/dev/null || { tail -20 "$RUN/oryxa.log"; echo "oryxa exited" >&2; exit 1; }
  sleep 1
done
curl -sf -o /dev/null "localhost:$API_PORT/healthz" || { tail -20 "$RUN/oryxa.log"; echo "oryxa did not come up" >&2; exit 1; }

cat <<DONE
API       http://localhost:$API_PORT
Auth stub http://localhost:$AUTHSTUB_PORT (also forwards to the API: point web-next here)
Mailpit   http://localhost:$MAILPIT_UI_PORT
Sign in   cookie oryxa_access=owner|admin|member|outsider, organization org_sim
Logs      $RUN
Web       ORYXA_URL=http://localhost:$AUTHSTUB_PORT pnpm --dir web-next exec vite dev --port $WEB_PORT
DONE
