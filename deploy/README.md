# Production deployment

One AWS account in us-west-2 runs every service in one ECS Fargate service,
behind Cloudflare; Vertex AI serves the models and Memory Bank; SES sends mail
from ap-south-1, where the domain has production access.

```
member ─► app.oryxa.in ─► Cloudflare ─┬─ /*    ─► Worker (web app)
                                       └─ /v1/* ─┐
Composio, Dodo ─► api.oryxa.in ─► Cloudflare ────┴─► Tunnel ─► cloudflared ┐
                                                                            │ one Fargate task, public
   ┌────────────────────────────────────────────────────────────────────────┘ subnet, no inbound rules
   ▼
 oryxa (every service) ──► PostgreSQL (RDS, private subnets)
   │   │   │                   ▲
   │   │   │                   └── cloudflared on the connector instance ◄── Tunnel ◄── Hyperdrive ◄── Workers
   │   │   └─► Vertex AI (Gemini, Memory Bank) as a service account, federated from the task role
   │   └─────► Bedrock (embeddings, rerank), S3, S3 Vectors, KMS, Cognito, SES (ap-south-1)
   └─► outbox ─► EventBridge (oryxa-production) ─► SQS per service ─► oryxa
 otel sidecar ─► X-Ray, CloudWatch (namespace Oryxa); logs ─► CloudWatch Logs; alarms ─► SNS ─► email
```

Nothing accepts a connection from the internet: the only ways in are the two
Cloudflare Tunnels, which the task and the connector instance dial out to.
Systems Manager (ECS Exec, Session Manager) is the administrative path.

## Who owns what

| Owner | What |
| --- | --- |
| `deploy/production` (OpenTofu) | VPC, database, KMS keys, ECR, ECS cluster, service and task definition, IAM, the event bus, log groups, dashboard, alarms, secrets' containers, the connector instance, the SES event forwarder, everything in Cloudflare (tunnels, DNS, Access, Hyperdrive, routes), and in Google Cloud the workload identity pool and service account |
| The services' provisioners (`deploy/scripts/provision-app.sh`) | Each service's event queue, dead-letter queue and EventBridge rules (generated from the code that consumes them), the files, artifacts and vector buckets, email's tables, queue and SES configuration set, auth's Cognito user pool |
| `deploy/scripts/memory-bank.sh` | The Vertex AI agent engine that holds Memory Bank |
| `deploy/scripts/composio-webhook.sh` | Composio's webhook subscription |
| `deploy/scripts/web.sh` (wrangler) | The web app's Worker |

OpenTofu names everything the scripts create, so the task's configuration and
IAM can refer to it before it exists. Its state is in S3
(`oryxa-tofu-state-<account>`), locked with a lock file.

## Before the first deploy

- AWS credentials for the account (`aws login`, or any profile), and
  `AWS_PROFILE` pointing at them.
- A Cloudflare API token for the account and the `oryxa.in` zone with: Zone
  DNS edit, Workers Routes edit, Cloudflare Tunnel edit, Access: Apps and
  Policies edit, Access: Service Tokens edit, Hyperdrive edit. Export it as
  `CLOUDFLARE_API_TOKEN`.
- Google Cloud Application Default Credentials for the project
  (`gcloud auth application-default login`), for OpenTofu and memory-bank.sh.
- A Google OAuth web client for sign-in, in `~/.config/oryxa/google-oauth.env`
  as `GOOGLE_OAUTH_CLIENT_ID=…` and `GOOGLE_OAUTH_CLIENT_SECRET=…`.
- `wrangler login`, Docker, Go, jq, OpenTofu.
- `COMPOSIO_API_KEY` in the repository's `.env`.

## First deploy

```sh
cd deploy/production && tofu init && tofu apply && cd -  # the platform
deploy/scripts/provision-app.sh     # queues, buckets, email, Cognito
deploy/scripts/put-secrets.sh       # Composio's API key
deploy/scripts/memory-bank.sh       # Memory Bank
(cd deploy/production && tofu apply) # Cognito and Memory Bank into the task
deploy/scripts/release.sh           # build, push, run one task
deploy/scripts/composio-webhook.sh  # triggers to api.oryxa.in
deploy/scripts/web.sh               # the web app at app.oryxa.in
```

Then add the `google_redirect_uri` provision-app.sh printed to the Google
OAuth client's authorized redirect URIs, and confirm the alarm subscription
from the email SNS sends. Commit the `*.auto.tfvars.json` files the scripts
wrote: they are what the next apply runs.

## Releasing

`deploy/scripts/release.sh` builds the image from the commit checked out,
pushes it tagged with that commit and applies it. The service starts a new
task, waits for its health check and for cloudflared to connect, then stops
the old one; one that never turns healthy is rolled back. The schema migrates
at start (`ORYXA_AUTO_MIGRATE`), under an advisory lock.

## Looking at it

- **Dashboard** `oryxa-production` in CloudWatch: the tunnel, API latency by
  route and status, the task, the database, every queue's backlog and dead
  letters, Bedrock, mail, and the newest errors.
- **Traces** in X-Ray: a request from the tunnel through every service it
  calls, the database, AWS and Google, and on into the events it published and
  the services that consumed them. A log line carries `trace_id` and
  `xray_trace_id`.
- **Logs**: `/oryxa-production/app` (JSON, one line per request and event),
  `/oryxa-production/cloudflared`, `/oryxa-production/otel`.
- **Database**: Performance Insights and Enhanced Monitoring in RDS; queries
  slower than 500 ms are logged to `/aws/rds/instance/oryxa-production/postgresql`.
- **Alarms** mail `alarm_email`: no running task, CPU or memory high, database
  CPU, storage, memory or connections, a queue backing up, anything in a
  dead-letter queue, the connector instance failing its checks.

A shell in the running task: `aws ecs execute-command --cluster
oryxa-production --task <id> --container oryxa --interactive --command sh`
(the image is distroless, so only `/oryxa` is there). The database from a
laptop, through the connector instance:

```sh
aws ssm start-session --target <instance id> \
  --document-name AWS-StartPortForwardingSessionToRemoteHost \
  --parameters host=<database address>,portNumber=5432,localPortNumber=15432
```

## At launch

- Aurora Serverless v2 in place of the t4g.micro instance: snapshot, restore
  into a cluster, point the `database-url` secret and Hyperdrive at it.
- A second task and Multi-AZ.
- A database user for Hyperdrive with only what Workers read, rather than the
  owner.
- Releases from CI with GitHub's OIDC rather than a laptop.
