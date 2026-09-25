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

Main releases from CI. Every pull request and every push to main runs
`.github/workflows/ci.yml`; once it passes on main,
`.github/workflows/deploy.yml` waits for approval on the repository's
`production` environment, and then:

1. builds the image for Fargate's arm64 and pushes it to ECR, tagged with the
   commit as `release.sh` would tag it;
2. registers the running task definition again with only that image changed,
   rolls the service onto it, and fails if the circuit breaker rolled it back;
3. records the tag in `release.auto.tfvars.json` on main, so the next
   `tofu apply` keeps that release rather than rolling back to the old one;
4. builds the web app and deploys its Worker.

It runs as `oryxa-production-github-deploy` (`production/github.tf`) through
GitHub's OIDC, which can push the image and roll the service and nothing
else, so infrastructure changes are still a `tofu apply` from a laptop. The
Actions tab's Run workflow releases main's head on demand.

Before its first run, once:

- `tofu apply` in `deploy/production`, which creates the GitHub OIDC provider
  and the role.
- In the repository's settings, an environment named `production`: yourself
  as required reviewer, and main as its only deployment branch. On a private
  repository GitHub offers required reviewers only on some plans; without one
  a release does not wait.
- In that environment, the secret `CLOUDFLARE_API_TOKEN` (the "Edit Cloudflare
  Workers" template, for this account) and the variable `HYPERDRIVE_ID`, from
  `tofu -chdir=deploy/production output -raw hyperdrive_id`.

A release from a laptop still works: `deploy/scripts/release.sh` builds the
image from the commit checked out, pushes it tagged with that commit and
applies it, and `deploy/scripts/web.sh` deploys the Worker.

Either way the service starts a new task, waits for its health check and for
cloudflared to connect, then stops the old one; one that never turns healthy
is rolled back. The schema migrates at start (`ORYXA_AUTO_MIGRATE`), under an
advisory lock.

### Releasing document access labels (#85)

The release that adds document access labels (Files → Knowledge → RAG,
`members` or `admins`) needs more care than the usual one. For a minute or two
the old task keeps serving a database the new one has already migrated. The old
build has no idea of access, so on that database it would show admin-only files
and passages to members. Its RAG inserts also fail, because the access column
no longer has a default. So, for this release only:

1. Stop the old task before the new one starts. That means one or two minutes
   of downtime instead of an overlap:

   ```sh
   aws ecs update-service --region us-west-2 --cluster oryxa-production --service oryxa \
     --deployment-configuration minimumHealthyPercent=0,maximumPercent=100
   ```

   Then release as usual. Afterwards, put the overlap back, either with
   `minimumHealthyPercent=100,maximumPercent=200` or with a `tofu apply`, which
   restores `compute.tf`'s values.
2. Once members or admins have created admin-only content, do not roll back to
   a build older than this one, because it would serve that content to
   members. If a later release has to be rolled back, roll back to a build
   that includes this one. If the circuit breaker rolls this release back,
   nothing admin-only exists yet. RAG ingestion stays stuck, though, with its
   inserts failing and knowledge events waiting in their queue, until this
   release goes out again.
3. What to expect afterwards:
   - Migration 010 requeues every document on the managed knowledge base, to
     add the `access` attribute. Members keep finding those documents during
     the resync, because their filter is "not admin-only", and PostgreSQL still
     checks access on every hit.
   - The ingest worker resubmits, within about five minutes, anything an older
     build submitted.
   - Knowledge republishes its Files documents with their labels on each
     source's first pass.
   - Documents an admin once added to an organization corpus from a Files file
     (source kind `file`) become admin-only. Re-add any that members should
     see.

### Releasing owner-approved agent requests

The release that holds agent requests made in a teamspace for their owner needs
the same care. An older build knows nothing of the hold. Its waker wakes the
addressed agent for a held request with the prompt to decide it, and its Decide
lets that agent approve it, so while an old task runs, or after a rollback, an
agent can approve what only its owner may. So, for this release only:

1. Stop the old task before the new one starts, exactly as for #85 above:

   ```sh
   aws ecs update-service --region us-west-2 --cluster oryxa-production --service oryxa \
     --deployment-configuration minimumHealthyPercent=0,maximumPercent=100
   ```

   Then release as usual, and put the overlap back afterwards
   (`minimumHealthyPercent=100,maximumPercent=200`, or a `tofu apply`).
2. Once a request is held for its owner (one made in a teamspace to a
   teammate's primary agent, or to a specialist contributed with `request`
   delegation), do not roll back to a build older than this one: its waker
   and Decide would let the addressed agent decide it. If a later release has
   to be rolled back, roll back to a build that includes this one. If the
   circuit breaker rolls this release back, no request is held yet.
3. What to expect afterwards:
   - The interaction migrations run in order, each in its own transaction:
     002 adds `teamspace_id` and `approval` with its check `NOT VALID`, 003
     validates that check (a scan that no longer runs under an ACCESS
     EXCLUSIVE lock), 004 builds the teamspace listing indexes, 005 adds the
     decider columns with their check `NOT VALID`, and 006 validates it. A
     database that already ran an earlier 002 (a development one) runs it
     again, because its checksum changed; every statement is safe to repeat.
   - A decision an older build recorded names no decider. The waker never
     treats such an approval of a held request as its owner's: it holds the
     request for the owner again instead of waking the agent.
   - A request made in a teamspace is read again, as its addressed member,
     before an agent decides it or is woken for it. Without a token of that
     member's, which needs `AGENTS_PRIMARY_SERVICE_KEY_ID` and the member's
     own "agents may act for me while I'm away" grant, a specialist with
     `automatic` delegation cannot decide it, so it is held for its owner
     instead. Expect that for members who have not granted it.
   - The web app's side conversations need this build before theirs: they
     send `teamspace_id`, list with `?teamspace_id=` and `?with_user=`, and
     create their threads with `origin: "side_conversation"`, all of which an
     older build refuses or ignores. They should also expect two new answers:
     `409 idempotency_key_reused` for a key sent again with another request,
     and `409 request_closed` for deciding a request that no longer stands in
     its teamspace.

### Releasing realtime, and turning it on

Realtime (`realtime/README.md`) has the web app's Worker purge a member's
direct messages and teamspace side conversations when they leave an
organization or it is deleted. It calls
`https://app.oryxa.in/internal/realtime/purge`, signed with
`REALTIME_PURGE_SECRET`, a secret the Worker holds too. It ships off, and
`realtime_purge` in `production/realtime.auto.tfvars.json`, committed as
`false`, turns it on.

**It stays off until web-next serves `app.oryxa.in`.** Production's
`app.oryxa.in` is served by the old `web/` Worker, which has no purge route and
answers `404`, so realtime turned on now would send every purge to its
dead-letter queue. web-next, which has the route, has no production
environment yet: its `wrangler.jsonc` has no `env.production`, so there is no
production Worker to give the secret to, and step 2 below waits for one.

Releasing realtime needs nothing first. A production start requires its queue
only once `REALTIME_WORKER_URL` or `REALTIME_PURGE_SECRET` is set, and while
`realtime_purge` is off neither is, so a release carrying realtime starts
whether or not an apply has given the task `REALTIME_EVENTS_QUEUE_URL`. The
task logs `realtime is off`, naming what is not set. The next `tofu apply`,
whenever it runs, gives the task the queue's variable, lets it read that queue,
adds the dead-letter queue's alarm and makes the secret's empty container. The
queue's backlog alarm waits for the switch, since nothing reads the queue until
then. Once `provision-app.sh` has made the queue and its rule, events wait
there, for up to 14 days each, for realtime to be turned on.

**Turning it on**, once web-next serves `app.oryxa.in` with
`POST /internal/realtime/purge`. In this order:

1. Make the secret, and put it in the repository's `.env` as
   `REALTIME_PURGE_SECRET=…`, exactly the 64 hex characters this prints, with
   no quotes or comment. `put-secrets.sh` refuses anything else:

   ```sh
   openssl rand -hex 32
   ```

2. Give the Worker the same 64 characters, in web-next's production
   environment, which does not exist yet, and deploy the Worker with the
   route:

   ```sh
   npx wrangler secret put REALTIME_PURGE_SECRET --env production
   ```

   The key is the value's characters as written, not decoded from hex. The
   Worker must take the body with `scope` and `removed_at`, and accept a
   `removed_at` that has a fraction of a second, which almost every real one
   has. A Worker built on the earlier body, without `scope`, must not be the
   one live when realtime is on. `realtime/README.md` has golden vectors to
   test the Worker's check against, one of them with a fraction.
3. Store the secret, then make the queue, its dead-letter queue and the rule
   that feeds it, if they are not there yet. The other provisioners run again
   too, and change nothing. `provision-app.sh` stops before provisioning
   anything if OpenTofu's state names no queue for a service yet, saying to
   apply first. The tasks cannot start while a secret they name holds no
   value, so store it before the next step:

   ```sh
   deploy/scripts/put-secrets.sh
   deploy/scripts/provision-app.sh
   ```

4. Switch it on, and apply. The task gets
   `REALTIME_WORKER_URL=https://app.oryxa.in` and the secret, and restarts
   with realtime on. The apply also adds the queue's backlog alarm, which may
   mail once while realtime works through the events that waited:

   ```sh
   echo '{"realtime_purge": true}' | jq . > deploy/production/realtime.auto.tfvars.json
   (cd deploy/production && tofu apply)
   ```

   Then commit the file, so an apply from any other checkout keeps realtime
   on.

5. Check it. The task logs `realtime mounted` with
   `worker_url=https://app.oryxa.in`. Remove a member from a test
   organization. Realtime then logs `realtime conversations purged`, and the
   Worker logs a `204`. `realtime purge dropped` means the Worker refused the
   purge's body, or its `removed_at` lay ahead; the log line carries the
   purge whole, and the reason. If the Worker never sees the call, look at
   Cloudflare's security events for `app.oryxa.in`: a bot or WAF challenge
   answers `403`. A `401` means the two secrets differ. Those purges, like a
   `404`'s, are retried into `oryxa-production-realtime-events-dlq`, whose
   alarm mails, and can be redriven once that is put right:

   ```sh
   aws sqs start-message-move-task --region us-west-2 \
     --source-arn "arn:aws:sqs:us-west-2:$(aws sts get-caller-identity --query Account --output text):oryxa-production-realtime-events-dlq"
   ```

To turn it off, set `realtime_purge` to `false`, apply and commit. The stored
secret stays, so turning it on again is step 4. The rule keeps filling the
queue, which holds each event for 14 days, and the backlog alarm goes with the
switch. Turned on again within those 14 days, realtime purges everything that
waited.

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
- Infrastructure from CI too: `tofu plan` on pull requests and `tofu apply`
  behind the same approval, which needs a broader role than releases do.
