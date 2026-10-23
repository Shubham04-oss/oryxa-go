# Dev stack, no AWS

One command runs Oryxa on a laptop or the dev box (`oryxa-a1`) without an AWS
account: LocalStack (EventBridge, SQS, S3, DynamoDB, KMS) and Moto (SES) for
AWS, the auth stub (`test/cmd/authstub`) for Cognito, and Vertex for Gemini and
rag's embeddings.

```sh
gcloud auth application-default login   # once; Gemini and embeddings need it
dev-stack/up.sh                         # start (again) and print the URLs
dev-stack/smoke.sh                      # check it end to end
dev-stack/down.sh                       # stop oryxa and the stub
```

Needs Docker with compose, Go, the AWS CLI, `jq` and `curl`.

`up.sh` starts the compose containers, provisions LocalStack again if it was
restarted (it keeps nothing), creates the dev KMS keys (service tokens, event
signatures, the agents and integrations sealing keys), seeds the stub's
organization `org_sim`, builds and starts the stub and `oryxa serve`. Logs and
binaries go to `dev-stack/.run/`. Run it again after a change.

## Signing in

The stub has no passwords: the token is a role name, `owner`, `admin`,
`member` or `outsider` (`test/identities`), sent as `Authorization: Bearer
member` or as the cookie `oryxa_access=member`. In a browser, set that cookie
on the web app's origin.

The stub also forwards whatever it does not answer to the API, so it is the one
address web-next needs: `ORYXA_URL=http://localhost:18080`. In the desktop
app, start `web-next-dev-stack` (stack on this machine) or
`web-next-dev-stack-a1` (stack on a1, over an SSH tunnel) from
`.claude/launch.json`.

## Settings

`.env.dev` holds the stack's settings over `.env.example`; neither has
secrets. Put your own in `dev-stack/.env.local` (ignored by git), for example:

```sh
TYPESAFE_API_KEY=...                    # Jev
COMPOSIO_API_KEY=...                    # then apps can run:
ORYXA_SERVICES=teams,billing,files,knowledge,rag,apps,batching,email,agents,workspace
GOOGLE_CLOUD_PROJECT=my-project
```

auth (Cognito) and realtime (the web app's Worker) do not run here.

## A second stack beside the first

Every port comes from `.env.dev`, so a second stack with its own containers
and database can run next to one that is already up:

```sh
cat > dev-stack/.env.local <<'EOF'
COMPOSE_PROJECT_NAME=oryxa-second
POSTGRES_PORT=5445
EVENTBRIDGE_PORT=4576
SES_PORT=4577
MAILPIT_SMTP_PORT=1035
MAILPIT_UI_PORT=8035
API_PORT=8180
AUTHSTUB_PORT=18180
EOF
```

The default compose project is `oryxa-go`, the same containers `docker
compose up` starts; enabling KMS in `docker-compose.yml` recreates its
LocalStack the first time, and `up.sh` provisions it again.
