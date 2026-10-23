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
| `deploy/production` (OpenTofu) | VPC, database, KMS keys, ECR, ECS cluster, service and task definition, IAM, the event bus, log groups, dashboard, alarms, secrets' containers, the connector instance, the SES event forwarder, everything in Cloudflare (tunnels, DNS, Access, Hyperdrive, routes, and the zone's WAF and rate-limit rules), and in Google Cloud the workload identity pool and service account |
| The services' provisioners (`deploy/scripts/provision-app.sh`) | Each service's event queue, dead-letter queue and EventBridge rules (generated from the code that consumes them), the files, artifacts and vector buckets, email's tables, queue and SES configuration set, auth's Cognito user pool |
| `deploy/scripts/memory-bank.sh` | The Vertex AI agent engine that holds Memory Bank |
| `deploy/scripts/composio-webhook.sh` | Composio's webhook subscription |
| `deploy/scripts/web.sh` (wrangler), or the Deploy workflow | The web app's Worker: web-next's `env.production`, `oryxa-production-web-next` |
| The Slack app, by hand at api.slack.com/apps | Created by the owner from `apps/internal/slackbot/manifest.yaml`; its credentials go to Secrets Manager with `deploy/scripts/put-secrets.sh` (see "The Slack bot") |

OpenTofu names everything the scripts create, so the task's configuration and
IAM can refer to it before it exists. Its state is in S3
(`oryxa-tofu-state-<account>`), locked with a lock file.

Each bucket's lifecycle rules have one owner, since S3 keeps one lifecycle
configuration per bucket and a put replaces it whole. The files and artifacts
buckets are the provisioners': every run of `provision-app.sh` puts their
rules again, including the one that removes the older versions a delete leaves
behind, so that rule is in their code. OpenTofu turns their versioning on
(`production/hardening.tf`), and owns the rules of the knowledge-base
documents bucket, which it creates.

## Before the first deploy

- AWS credentials for the account (`aws login`, or any profile), and
  `AWS_PROFILE` pointing at them.
- A Cloudflare API token for the account and the `oryxa.in` zone with: Zone
  DNS edit, Workers Routes edit, Cloudflare Tunnel edit, Access: Apps and
  Policies edit, Access: Service Tokens edit, Hyperdrive edit, Zone WAF edit
  (for the custom and rate-limiting rules in `production/hardening.tf`).
  Export it as `CLOUDFLARE_API_TOKEN`.
- Google Cloud Application Default Credentials for the project
  (`gcloud auth application-default login`), for OpenTofu and memory-bank.sh.
- A Google OAuth web client for sign-in, in `~/.config/oryxa/google-oauth.env`
  as `GOOGLE_OAUTH_CLIENT_ID=…` and `GOOGLE_OAUTH_CLIENT_SECRET=…`.
- `wrangler login`, Docker, Go, jq, OpenTofu.
- `COMPOSIO_API_KEY` in the repository's `.env`.

## First deploy

```sh
cd deploy/production && tofu init && tofu apply -var first_deploy=true && cd -  # the platform
deploy/scripts/provision-app.sh     # queues, buckets, email, Cognito
deploy/scripts/put-secrets.sh       # Composio's API key
deploy/scripts/memory-bank.sh       # Memory Bank
(cd deploy/production && tofu apply) # Cognito and Memory Bank into the task
deploy/scripts/release.sh           # build, push, run one task
deploy/scripts/composio-webhook.sh  # triggers to api.oryxa.in
deploy/scripts/web.sh               # the web app at app.oryxa.in
```

`first_deploy=true` belongs to that first apply alone. The files and artifacts
buckets are made by `provision-app.sh`, after it, so their versioning cannot be
turned on yet; the apply that follows the script, run without the flag, does.
The flag also has OpenTofu create the database's two log groups itself, before
the instance. Without it, OpenTofu adopts the ones RDS made (`import` blocks in
`production/hardening.tf`), and an empty account has none to adopt.

Then add the `google_redirect_uri` provision-app.sh printed to the Google
OAuth client's authorized redirect URIs, and confirm the alarm subscription
from the email SNS sends. Commit the `*.auto.tfvars.json` files the scripts
wrote: they are what the next apply runs.

## Releasing

Main releases from CI. Every pull request and every push to main runs
`.github/workflows/ci.yml`; once it passes on main,
`.github/workflows/deploy.yml` runs in the repository's `production`
environment, which only main may deploy to, and with no required reviewer
releases every push to main that passes CI:

1. builds the image for Fargate's arm64 and pushes it to ECR, tagged with the
   commit as `release.sh` would tag it;
2. registers the running task definition again with only that image changed,
   rolls the service onto it, and fails if the circuit breaker rolled it back;
3. records the tag in `release.auto.tfvars.json` on main, so the next
   `tofu apply` keeps that release rather than rolling back to the old one;
4. builds web-next as its production Worker and deploys it.

It runs as `oryxa-production-github-deploy` (`production/github.tf`) through
GitHub's OIDC, which can push the image and roll the service and nothing
else, so infrastructure changes are still a `tofu apply` from a laptop. The
Actions tab's Run workflow releases main's head on demand.

Every action in both workflows is pinned to a full commit SHA, with its
release in a comment beside it, so a tag that is moved cannot change what a
release runs. To update one, look up the new release's tag with
`gh api repos/<owner>/<repo>/git/ref/tags/<tag>`, and if its object is a
`tag` rather than a commit, read the commit from `git/tags/<sha>`; then change
the SHA and the comment together.

Before its first run, once:

- `tofu apply` in `deploy/production`, which creates the GitHub OIDC provider
  and the role.
- In the repository's settings, an environment named `production` with main
  as its only deployment branch. A required reviewer would make every release
  wait for an approval; on a private repository GitHub offers one only on some
  plans.
- In that environment, the secret `CLOUDFLARE_API_TOKEN` (the "Edit Cloudflare
  Workers" template, for this account and the zone).
- In that environment, the secret `RELEASE_DEPLOY_KEY`: the private half of the
  repository's write deploy key "deploy: record release (Deploy workflow)". The
  "main: CI must pass" ruleset requires CI on main and lets only that key push
  past it, which is how a release records itself. To rotate it, add a new
  deploy key, replace the secret, add the key to the ruleset's bypass list, then
  remove the old key.

A release that needs a step first, like the ones below, needs it before its
pull request merges, since merging releases it.

A release from a laptop still works: `deploy/scripts/release.sh` builds the
image from the commit checked out, pushes it tagged with that commit and
applies it, and `deploy/scripts/web.sh` deploys the Worker.

The old `web/` app's Worker, `oryxa-production-web`, stays deployed as it last
was, and nothing deploys it any more. Setting `web_worker_name` in
`production/web.auto.tfvars.json` back to it and applying routes
`app.oryxa.in` to the old app again.

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
   of downtime instead of an overlap. ECS refuses `maximumPercent=100` while
   the service rebalances across Availability Zones, which it does by default,
   so that goes off for the release too:

   ```sh
   aws ecs update-service --region us-west-2 --cluster oryxa-production --service oryxa \
     --availability-zone-rebalancing DISABLED \
     --deployment-configuration minimumHealthyPercent=0,maximumPercent=100
   ```

   Then release as usual. Afterwards, put the overlap back, either with
   `minimumHealthyPercent=100,maximumPercent=200` or with a `tofu apply`, which
   restores `compute.tf`'s values, and then the rebalancing, which OpenTofu
   leaves alone:

   ```sh
   aws ecs update-service --region us-west-2 --cluster oryxa-production --service oryxa \
     --availability-zone-rebalancing ENABLED
   ```
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

1. Stop the old task before the new one starts, exactly as for #85 above,
   rebalancing off with it:

   ```sh
   aws ecs update-service --region us-west-2 --cluster oryxa-production --service oryxa \
     --availability-zone-rebalancing DISABLED \
     --deployment-configuration minimumHealthyPercent=0,maximumPercent=100
   ```

   Then release as usual, and put the overlap back afterwards
   (`minimumHealthyPercent=100,maximumPercent=200`, or a `tofu apply`), and the
   rebalancing (`--availability-zone-rebalancing ENABLED`).
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

### Releasing faster agent requests

The release that wakes an agent as soon as a request reaches it, and closes
approved requests that will not be worked, needs no special steps: an old and
a new task can overlap, and a rollback is safe. What to expect afterwards:

- Interaction migration 007 builds `agent_interactions_approved_expiry_idx`,
  a small partial index of approved requests by expiry, under a SHARE lock
  that lets reads go on.
- Every poll of the waker closes approved requests whose expiry passed
  unanswered within the last hour: failed, saying they expired, with the
  completed event that tells the member who asked. The first poll after the
  release closes only those that ran out in the hour before it. Every
  approval that expired earlier is left as it was: it reads as `expired`, as
  before, with no event, and its `updated_at` does not change, so old
  requests keep their place in every listing and wake nobody.
- Approved work the waker gives up on, after three failed runs or because
  its agent was deleted, is closed the same way.
- An agent answering a delivered request ends its run on the recorded
  answer, so its delivery thread no longer ends with a closing message.

### Releasing realtime, and turning it on

Realtime (`realtime/README.md`) has the web app's Worker purge a member's
direct messages and teamspace side conversations when they leave an
organization or it is deleted. It calls
`https://app.oryxa.in/internal/realtime/purge`, signed with
`REALTIME_PURGE_SECRET`, a secret the Worker holds too. It also serves
`POST /v1/realtime/events`, where the Worker, signing with the same secret,
sends the messages posted and conversations read in those conversations, for
realtime to publish on the bus as `oryxa.realtime`. It was turned on in
production on 2026-09-28: `realtime_purge` in
`production/realtime.auto.tfvars.json` is `true`, and the route is live
with it.

**The Worker's events need the Worker configured and realtime on.**
web-next's Worker needs `ORYXA_API_ORIGIN`, the API's origin
(`https://app.oryxa.in`, whose `/v1/*` reaches the tasks through the tunnel),
and the same `REALTIME_PURGE_SECRET`; without either it records nothing.
Realtime serves the route only while it is on: until then the Worker's calls
are answered `404`, and it retries each event for seven days, then drops it.
Nothing else is needed: the task role may already put events from any source
on the bus, nothing consumes realtime's events yet, so no rule or queue is
added, and realtime's tables are migrated by every release, on or off.

**web-next now serves `app.oryxa.in` with the purge route.** The old
`web/` Worker has no purge route and answers `404`, so realtime turned on
while it served `app.oryxa.in` would have sent every purge to its
dead-letter queue. web-next's production Worker,
`oryxa-production-web-next` (`env.production` in its `wrangler.jsonc`), has
the route; the Deploy workflow deploys it, and
`production/web.auto.tfvars.json` routes `app.oryxa.in` to it. That has
been true since web-next's release, which is what let realtime turn on.

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

**Turning it on.** Realtime was turned on in production on 2026-09-28, in
exactly this order, once web-next was serving `app.oryxa.in` with
`POST /internal/realtime/purge`:

1. Make the secret, and put it in the repository's `.env` as
   `REALTIME_PURGE_SECRET=…`, exactly the 64 hex characters this prints, with
   no quotes or comment. `put-secrets.sh` refuses anything else:

   ```sh
   openssl rand -hex 32
   ```

2. Give the Worker the same 64 characters, in web-next's production
   environment, once the Worker with the route is deployed:

   ```sh
   npx wrangler secret put REALTIME_PURGE_SECRET --env production
   ```

   The key is the value's characters as written, not decoded from hex. The
   Worker must take the body with `scope` and `removed_at`, and accept a
   `removed_at` that has a fraction of a second, which almost every real one
   has. A Worker built on the earlier body, without `scope`, must not be the
   one live when realtime is on. Nor may one without the teamspace scopes,
   `teamspace_member` and `teamspace`, with `teamspace_id`: it refuses each
   with a `400`, which realtime takes as for good, so the purge is dropped,
   logged whole as `realtime purge dropped`, and the side conversations stay. `realtime/README.md` has golden vectors to
   test the Worker's check against, one of them with a fraction, and golden
   vectors for the Worker's own signing of its events. Give the Worker
   `ORYXA_API_ORIGIN=https://app.oryxa.in` too, as web-next's `REALTIME.md`
   says, so it sends them.
3. Store the secret, then make the queue, its dead-letter queue and the rule
   that feeds it, if they are not there yet, or bring the rule up to date: it
   matches agents' teamspace endings as well as auth's events since the
   teamspace purges were added. The other provisioners run again too, and
   change nothing. `provision-app.sh` stops before provisioning
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
   alarm mails, and can be redriven once that is put right. Send a direct
   message: realtime logs `realtime events accepted`, and a refusal of the
   Worker's call as `the Worker's events refused`, with the reason:

   ```sh
   aws sqs start-message-move-task --region us-west-2 \
     --source-arn "arn:aws:sqs:us-west-2:$(aws sts get-caller-identity --query Account --output text):oryxa-production-realtime-events-dlq"
   ```

These same steps are how to turn realtime on again after it is turned off,
and how to rotate the secret: put the new value in the repository's `.env`
and Secrets Manager together (`deploy/scripts/put-secrets.sh`), give the
Worker the same value (`wrangler secret put REALTIME_PURGE_SECRET --env
production` from web-next), then apply.

To turn it off, set `realtime_purge` to `false`, apply and commit. The stored
secret stays, so turning it on again is step 4. The rule keeps filling the
queue, which holds each event for 14 days, and the backlog alarm goes with the
switch. Turned on again within those 14 days, realtime purges everything that
waited.

### Releasing the answer guard

The release that has Jev choose the tools a teamspace request needs, limits
approved work to the tools its approval allows, and has Bedrock read every
answer before the member who asked sees it (`agents/primary/README.md`,
"Answer guard"). Release the build first, then apply. The build alone
changes little: with no guardrail named the guard is off, and answers are
delivered as before. The apply turns the guard on:

```sh
(cd deploy/production && tofu apply)
```

It creates the guardrail `oryxa-production-agent-answers`
(`production/answer_guard.tf`) in us-west-2: sensitive information on the
output side only, input off, and no cross-region profile, masking secrets and
card, bank and government numbers, and only detecting email addresses, phone
numbers and addresses. It also creates its numbered version, lets the task
role call `bedrock:ApplyGuardrail` on that guardrail alone, and names both to
the task as `AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID` and
`AGENTS_PRIMARY_ANSWER_GUARDRAIL_VERSION`. The task then restarts with the
guard on, and logs `the answer guard checks every answer another member reads
before they read it`, naming the guardrail and its version. The service
refuses to start with one of the two set and not the other; the apply always
sets both. A later change to the guardrail makes a new version, and the next
apply names it; the old version is kept.

This guard was released and applied in production on 2026-09-28.

In that order, no task older than this build ever reads the events the guard
brings: an answer held for review is the completed event with status
`review`, and the end of a review one with `completed`, `failed` or
`withheld`. An older build's consumer refuses those statuses, so an event it
took during an overlap would go back to the queue, and after five tries to
`oryxa-production-agents-events-dlq`, from which it can be redriven.

What to expect afterwards:

- Interaction migrations 008 to 012 run in order, each in its own
  transaction. 008 adds `tools_judged_by` with its check `NOT VALID`, and 009
  validates it. 010 adds `guard`, `held_result` and `review`, and replaces
  001's status check with one that also allows `review` and `withheld`, in
  the same transaction, all its checks `NOT VALID`; 011 validates them. 012
  builds `agent_interactions_review_expiry_idx`, a small partial index of
  requests in review. Every row predating them reads as before.
- Approved work runs with exactly the toolboxes its approval allows. An
  approval that allowed none, which is every approval the web app has sent so
  far, now runs with the request alone, where it had the member's read-only
  apps and knowledge. That includes approved requests in flight at the
  release.
- A request made in a teamspace that names no tools has Jev choose them, with
  `TYPESAFE_API_KEY`, which production has for the knowledge judge. Its owner's
  approval shows what will be read.
- An answer holding an email address, phone number or address, or one the
  guardrail could not check, waits in `review` for the member it asked, and
  the member who asked sees only that status. The web app shows review and
  withheld requests, and calls the review route, from its own release on.
  Until then the member reviews nothing, and the waker withholds each such
  answer when its request expires, a day after it was made by default.
- Rolling back to an older build is safe but leaves answers in review where
  they are: it shows the status and no result to either side, has no review
  route, and withholds nothing when they expire. This build, back again,
  withholds those that expired meanwhile on its first poll.
- Each answer read costs up to three ApplyGuardrail calls (summary, content,
  artifacts), each billed by Bedrock by the characters it reads, with a
  minimum per call.

### Oryxa's own sign-in

Work package 1F (`docs/design/integrations-plan.md`) gives `apps` a KMS vault
and the settings its own sign-in needs (`apps/README.md`, "Oryxa's own
sign-in"), which still ships switched off. Apply it targeted, so it picks up
nothing else pending in this configuration, in this order:

```sh
(cd deploy/production && tofu apply \
  -target=aws_kms_key.apps_signin -target=aws_kms_alias.apps_signin \
  -target=aws_iam_role_policy.task -target=aws_iam_role_policy.execution_secrets \
  -target=aws_secretsmanager_secret.external \
  -target=aws_ecs_task_definition.oryxa -target=aws_ecs_service.oryxa)
deploy/scripts/put-secrets.sh   # APPS_DEFAULT_OAUTH_CLIENTS, APPS_HOOK_SECRETS
echo '{"apps_signin_secrets_ready": true}' | jq . > deploy/production/apps_signin.auto.tfvars.json
(cd deploy/production && tofu apply \
  -target=aws_ecs_task_definition.oryxa -target=aws_ecs_service.oryxa)
```

The first apply creates `alias/oryxa-production-apps-signin` (a 30-day
deletion window and `prevent_destroy`, since losing it loses every sealed
credential for good), lets the task role `kms:GenerateDataKey` and
`kms:Decrypt` on that key alone, conditioned on
`kms:EncryptionContext:service = oryxa-apps`, gives the task
`APPS_SIGNIN_KMS_KEY_ID` and `APPS_SIGNIN_REDIRECT_URI`
(`https://app.oryxa.in/v1/apps/oauth/callback`), and makes empty containers
for the two secrets below — but `APPS_ORYXA_TOOLKITS` stays `""`, so `apps`
still refuses every Oryxa connection and Composio keeps serving everything.
An ECS task whose definition names a secret with no value fails to start, so
the task definition does not reference `APPS_DEFAULT_OAUTH_CLIENTS` or
`APPS_HOOK_SECRETS` until `apps_signin_secrets_ready` says `put-secrets.sh`
has filled them in; commit `apps_signin.auto.tfvars.json` once it is `true`,
so every other checkout's apply keeps naming them.

The first apply names `aws_iam_role_policy.execution_secrets` because the task
definition does not depend on it: it is the policy that lets ECS read each
secret the definition names as a task starts, and it lists the secrets'
containers, so a targeted apply that leaves it out makes a task that names a
secret it may not read. That task fails to start and the deployment is rolled
back. If you applied this section before that target was in it, run
`(cd deploy/production && tofu apply -target=aws_iam_role_policy.execution_secrets)`
before turning `apps_signin_secrets_ready` on.

**Switching a toolkit on** is the plan's gate G4: change
`APPS_ORYXA_TOOLKITS = ""` in `compute.tf` to a comma list naming it
(`"github"`, `"github,slack"`, ...) and apply. First, register
`https://app.oryxa.in/v1/apps/oauth/callback` — exactly, including scheme and
path — as the redirect URI on that toolkit's own OAuth app at GitHub, Slack
or Notion; a mismatched URI sends the member's browser somewhere the
provider refuses or apps does not recognize.

### The Slack bot

The Slack consent loop (`docs/design/slack-implementation.md`) lets a member
@-mention Oryxa in a channel to ask a question. Oryxa asks the person who would
know, in a direct message with three buttons (Share answer, Allow for 1 hour,
or Deny), and their agent answers in the asker's thread only if they approve.
When the answer is a credential, its owner types it into Oryxa and the member
who asked opens it there, once; it never goes through Slack.

It ships off, behind one switch: `slack_app_ready`, declared in
`production/secrets.tf`, `false` unless `production/slack.auto.tfvars.json` says
otherwise. Off, no Slack route is served and nobody is asked for anything in
Slack. On, four things change together, because none works without the others:

| | `slack_app_ready` false | true |
| --- | --- | --- |
| `APPS_SLACK_SURFACE`, apps serves the Slack routes | `false` | `true` |
| `APPS_SERVICE_CALLERS`, who may call apps' internal routes | not set | agents, for the purpose `slack` and no other |
| `AGENTS_PRIMARY_SLACK_CONSENT`, agents asks owners for consent in Slack | `off` | `on` |
| Secrets the task names | neither | `APPS_SLACK_APP` and `AGENTS_PRIMARY_SLACK_TICKET_KEY` |

`AGENTS_PRIMARY_SECRETS_KMS_KEY_ID`, the key one-time secrets are sealed under,
is set either way: a secret can only be shared for a request approved with
access, which nothing makes until consent is on.

What `production` adds for it:

- **`alias/oryxa-production-agents-secrets`** (`keys.tf`), the KMS key one-time
  secrets are sealed under, with a 30-day deletion window and `prevent_destroy`
  like its siblings. The task role may `kms:GenerateDataKey` and `kms:Decrypt`
  on it only with the encryption context `service = oryxa-agents`. A secret waits
  in `agents_primary.secret_shares` for an hour at most, as ciphertext only,
  sealed with a data key of its own that is bound to its request. The member who
  asked opening it, its owner revoking it, or the hour ending deletes it, and a
  sweeper deletes what expired unopened. Losing the key would cost only the
  secrets waiting at that moment, which their owners share again.
- **Two secrets' containers** (`secrets.tf`), which exist whatever the switch
  says: `oryxa-production/slack-app`, the Slack app's Client ID, Client Secret
  and Signing Secret as JSON, `{"client_id","client_secret","signing_secret"}`;
  and `oryxa-production/slack-ticket-key`, 64 hex characters, the HMAC key that
  signs the buttons on owners' notices, so that a forged click made with a leaked
  signing secret decides nothing.
- Nothing else: no queue, no bucket, no Cloudflare rule. The screen of what
  people ask in Slack uses Jev, so it needs `TYPESAFE_API_KEY`, which
  production has already for the knowledge judge (`knowledge_judge: jev`):
  without one every mention is refused as `screen_failed`, and nobody is asked.
  Agents also needs its consent guardrail (`answer_guard.tf`), applied before
  the release that names it: without permission on it, every answer to a Slack
  question is held as unchecked. Apps seals the bot's
  token with the key it already has for "Oryxa's own sign-in"
  (`APPS_SIGNIN_KMS_KEY_ID`), and the rule that blocks `/v1/internal` at the
  edge stays, because agents calls apps' internal routes from inside the task
  (`APPS_URL` is the process itself).

**First, with the switch off, before the release that carries the bot merges.**
Merging to main releases, so a release's infrastructure goes in before it, never
after. This makes the key, the task role's permission on it and the two empty
containers, and rolls the task once, as any change to its definition does; the
task does nothing new. Run it from a checkout that has main's
`release.auto.tfvars.json` (main's head, or this branch after main is merged
into it), which names the release that is running: an older copy would roll the
service back to an older image. Read the plan first. It should show the key, its
alias, the two containers, the consent guardrail and its version
(`answer_guard.tf`, which the task role's policy and the task definition now
name, so a target on either takes it), the task role's and the execution role's
policies and a new task definition, and nothing else, and the definition's image
should not change. The execution role's is the one that lets ECS read a secret the
definition names as a task starts, and the task definition does not depend on it,
so the targets name it: without it the switch below makes a task that names two
secrets it may not read, which fails to start and is rolled back.

```sh
git pull
(cd deploy/production && tofu apply \
  -target=aws_kms_key.agents_secrets -target=aws_kms_alias.agents_secrets \
  -target=aws_secretsmanager_secret.external \
  -target=aws_iam_role_policy.task -target=aws_iam_role_policy.execution_secrets \
  -target=aws_ecs_task_definition.oryxa -target=aws_ecs_service.oryxa)
```

**The owner's steps, in order,** once the release is out. The app is made and
checked in Slack's own site, by its owner (steps 1, 5 and 6), and the rest are
commands:

1. **Create the app from the manifest.** At https://api.slack.com/apps choose
   Create New App, then From a manifest, pick the workspace to develop it in,
   and paste `apps/internal/slackbot/manifest.yaml`. The manifest is the only
   way the app's scopes, events and URLs change: edit the file (and
   `manifest_test.go`, which pins it), never Slack's settings. From Basic
   Information keep the Client ID, Client Secret and Signing Secret in a
   password manager, not in chat.
2. **Store them.**

   ```sh
   deploy/scripts/put-secrets.sh
   ```

   It asks for the Client ID, the Client Secret and the Signing Secret at
   hidden prompts that never echo what you type (or reads `APPS_SLACK_APP` from
   `.env` as the JSON above, or the environment), checks them the way apps will
   when it starts, and stores them as the one JSON value. It also makes the
   ticket key with `openssl rand -hex 32` when none is stored, and leaves the one
   that is: a new key makes every button already posted refuse its click. Nothing
   is printed or put on a command line. It needs the containers from the apply
   above, and says so if they are missing. To see that a secret has a value
   without reading it, `aws secretsmanager describe-secret --secret-id
   oryxa-production/slack-app --query VersionIdsToStages` shows a version staged
   `AWSCURRENT`.
3. **Turn it on and apply.**

   ```sh
   echo '{"slack_app_ready": true}' | jq . > deploy/production/slack.auto.tfvars.json
   (cd deploy/production && tofu apply \
     -target=aws_iam_role_policy.execution_secrets \
     -target=aws_ecs_task_definition.oryxa -target=aws_ecs_service.oryxa)
   ```

   Commit `slack.auto.tfvars.json`, so that every other checkout's apply keeps
   the switch on. The service rolls a new task. Apps refuses to start with the
   Slack surface on and an `APPS_SLACK_APP` it cannot read, and ECS then rolls
   the deployment back and the apply fails, rather than leaving a task that does
   not serve; `put-secrets.sh` has checked the value already, so that should not
   happen. `/oryxa-production/app` logs `the Slack surface is on` with the three
   URLs it serves.
4. **Provision the events.** `deploy/scripts/provision-app.sh` makes each
   service's EventBridge rules from what it subscribes to, so run it after the
   release that adds subscriptions: agents' queue starts receiving apps'
   `apps.slack.message.received.v1`, `apps.slack.action.received.v1` and
   `apps.slack.install.changed.v1` (a workspace's install ending, which has
   agents forget it), and realtime's queue starts receiving agents'
   `agents.interaction.*` request events. Without it those events are not
   delivered, and nothing fails to start.

   **Run it only once the release has finished rolling out, never before.** A
   rule is matched by exact detail type, so widening it is what starts
   delivering them, at once; a task that does not know a type fails it ("no
   handler"), and after five receives the queue moves it to the dead-letter
   queue, whose alarm mails. Agents' consumer knows the three from this release
   on, and with `AGENTS_PRIMARY_SLACK_CONSENT` off it acknowledges and drops
   them, so widening the rule while the switch is off is harmless, though it
   feeds the queue for nothing. The order is therefore: the Worker (realtime
   needs the web app's release), the release, then this. Confirm the running
   task's image is the new one (`aws ecs describe-services`, one deployment,
   `PRIMARY`) before running it.
5. **Verify both request URLs in Slack's settings.** Slack does not check them
   when it creates an app from a manifest.
   - Event Subscriptions: the Request URL,
     `https://api.oryxa.in/v1/apps/slack/events`, must show Verified. Press
     Retry: Slack sends a `url_verification` challenge, and apps answers it once
     the delivery's signature checks out, which is also the first proof that the
     Signing Secret is right.
   - Interactivity & Shortcuts: Interactivity is on and its Request URL is
     `https://api.oryxa.in/v1/apps/slack/interactivity`. Slack does not test it;
     the first click on a button does.
   - OAuth & Permissions: the Redirect URL is
     `https://api.oryxa.in/v1/apps/slack/install/callback`.
6. **Install through Oryxa's Add to Slack, never Slack's own Install button.**
   As an owner or admin of the organization, choose Add to Slack in Oryxa's
   Slack settings, approve in Slack, and Oryxa brings you back. Do not press
   Install to Workspace in the app's Install App page. Only Oryxa's own callback
   receives the bot's token, seals it and ties the workspace to the
   organization; Slack's button gives Oryxa nothing, and a workspace with no
   install in Oryxa has every mention of the bot acknowledged and dropped.

After that, the live-run checklist is `docs/design/slack-implementation.md`,
section 5.

**Other workspaces.** An app installs only where it was made until it is
distributed. To install in another workspace, open Manage Distribution and
Activate Public Distribution, which gives a beta install link; a Marketplace
listing comes later. An organization serves one workspace, a workspace one
organization, and Enterprise Grid installs are refused for now.

**Turning it off, and changing what was stored.**

- Off: put `{"slack_app_ready": false}` in `slack.auto.tfvars.json` and apply as
  in step 3. The task stops naming the secrets and serving the routes; Slack's
  deliveries then get 404s, and Slack retries and, if they keep failing,
  disables the app. Ending an install is `DELETE
  /v1/organizations/{org}/apps/slack/install`, or removing the app in Slack.
- The Signing Secret or Client Secret changes in Slack: put the new JSON in
  `.env` as `APPS_SLACK_APP`, run `put-secrets.sh`, and start a new task
  (`aws ecs update-service --region us-west-2 --cluster oryxa-production
  --service oryxa --force-new-deployment`), which reads secrets only as it
  starts. Apps holds one Signing Secret at a time, so a delivery in flight at
  the switch fails its signature and Slack retries it.
- The ticket key: the same with `AGENTS_PRIMARY_SLACK_TICKET_KEY=<64 hex>`. Every
  button already posted stops working, and its owner decides from the request in
  Oryxa instead.
- Token rotation stays off in the manifest. Slack turns it on for good, and
  nothing here refreshes a token yet; the install refuses one that rotates.
- Slack expects an answer to each delivery within three seconds and retries one
  that gets none (at once, then after one minute and after five), and it
  disables an app whose deliveries mostly fail for an hour. Apps answers from
  the database alone, so what would do that is the database being away: the
  application-error alarm is the sign.

### Applying the launch-review hardening

`production/hardening.tf`, with small edits in `compute.tf`, `database.tf`,
`keys.tf`, `knowledge_base.tf`, `observability.tf` and `variables.tf`, and a
lifecycle rule in the files and artifacts provisioners, answers the findings of
the 2026-09-28 launch review (`docs/REMAINING-WORK.md`, section 7):
recoverable files, more alarms, a 30-day expiry on the database's log groups,
keys that cannot be destroyed, a narrower Cognito grant, a pinned collector
image, and two Cloudflare rules. It was written without applying anything. The
apply is a plain `tofu apply`, and removes nothing but the task definition's
old revision. Before it:

1. **Run `deploy/scripts/provision-app.sh` first,** from a checkout with this
   change. It puts the rule that removes older versions on the files and
   artifacts buckets. They are not versioned yet, and S3 documents that rule as
   having no effect on a bucket that is not. The apply then turns versioning on
   with the rule already in place. In the other order, older versions in those
   two buckets are not removed until the script has run.
2. **The API token has Zone WAF edit** (see "Before the first deploy"), or the
   two rulesets fail.
3. **The zone has no rules of its own at Cloudflare yet.** A zone has one
   entry-point ruleset for each phase, and OpenTofu takes each over whole. If
   the dashboard's Security, WAF, Custom rules or Rate limiting rules already
   hold anything, creating these fails, or replaces it. Import that ruleset
   instead, and add its rules to `hardening.tf` so that the apply keeps them.
   Its id is in the answer to
   `GET /zones/<zone id>/rulesets/phases/<phase>/entrypoint`:

   ```sh
   tofu import cloudflare_ruleset.custom_rules zones/<zone id>/<ruleset id>
   tofu import cloudflare_ruleset.rate_limits zones/<zone id>/<ruleset id>
   ```

The apply rolls the service once, because the collector's image is pinned to a
release and its digest, in a new task definition. Once it is done:

- Confirm the subscription mail from the SES alarms' own topic, and set one of
  them to ALARM to see that the mail arrives. They are in ap-south-1, and an
  alarm can only notify a topic in its own region, so they notify
  `oryxa-production-alarms` there, which mails the same address:

  ```sh
  aws cloudwatch set-alarm-state --region ap-south-1 \
    --alarm-name oryxa-production-ses-complaints \
    --state-value ALARM --state-reason test
  ```

- Deletes from the files, artifacts and knowledge-base documents buckets are
  now soft for 30 days (see "Recovering a deleted file").

### SpiceDB, in shadow

Permissions step 4 (`docs/design/permissions/README.md`, §6) runs SpiceDB
beside `internal/access`, which still decides. `production/spicedb.tf` holds
all of it, behind two switches:

- `spicedb_enabled` makes everything but runs nothing: a `spicedb` login and
  database URI (Secrets Manager), the container for `SPICEDB_PRESHARED_KEY`, a
  log group, a security group open on 50051 to the oryxa tasks only, a private
  DNS name (`spicedb.oryxa-production.internal`, Cloud Map), the service, two
  one-off task definitions (`-spicedb-migrate`, `-spicedb-schema`), the
  projector's queue (`oryxa-production-spicedb-events`, its `-dlq`, and the
  EventBridge rule `spicedb-events`, built from `projector.Subscriptions()`),
  and `track_commit_timestamp = on` in the database's parameter group, which
  waits for a reboot. Events collect in the queue until the projector runs.
- `spicedb_desired_count` above zero starts that many tasks (two), adds an
  alarm when fewer run, and gives the oryxa task `SPICEDB_ENDPOINT`,
  `SPICEDB_INSECURE=true`, `SPICEDB_PRESHARED_KEY` and
  `SPICEDB_EVENTS_QUEUE_URL`, the right to consume that queue, its backlog and
  dead-letter alarms, and an alarm on the nightly reconcile's fixes. The
  endpoint and the queue come together: production refuses to start with
  `SPICEDB_ENDPOINT` set and no queue (`ORYXA_REQUIRE_EVENT_QUEUES`).

With both off, as committed, the plan shows no change. gRPC is plain inside
the VPC: only the oryxa tasks' security group reaches port 50051, and every
call carries the preshared key.

The one-off tasks run with the oryxa service's own network, as
`scripts/sql.sh` does. Set this once for the steps below:

```sh
cluster=$(tofu -chdir=deploy/production output -raw cluster)
network=$(aws ecs describe-services --cluster "$cluster" --services oryxa \
  --query 'services[0].networkConfiguration' --output json)
run_once() {  # run_once <family>: runs it, waits, prints each container's exit code
  task=$(aws ecs run-task --cluster "$cluster" --launch-type FARGATE --task-definition "$1" \
    --network-configuration "$network" --query 'tasks[0].taskArn' --output text)
  aws ecs wait tasks-stopped --cluster "$cluster" --tasks "$task"
  aws ecs describe-tasks --cluster "$cluster" --tasks "$task" \
    --query 'tasks[0].containers[].[name,exitCode,reason]' --output table
}
```

Their output is in the log group `/oryxa-production/spicedb`.

1. **Turn it on, with no task yet,** and commit the file so every checkout's
   apply agrees:

   ```sh
   echo '{"spicedb_enabled": true, "spicedb_desired_count": 0}' | jq . > deploy/production/spicedb.auto.tfvars.json
   ```

2. **Put the secrets.** The preshared key's container has to exist first; then
   `put-secrets.sh` makes the key once (and leaves it on later runs). The
   datastore URI needs nothing: OpenTofu writes it, as it does `DATABASE_URL`.

   ```sh
   (cd deploy/production && tofu apply -target=aws_secretsmanager_secret.external)
   deploy/scripts/put-secrets.sh
   ```

3. **Apply.** Targeted, so it picks up nothing else pending. The plan should
   list only SpiceDB's resources, the parameter group's one new parameter, and
   the execution role's policy (which gains the datastore URI); the oryxa task
   definition does not change yet.

   ```sh
   (cd deploy/production && tofu apply \
     -target=aws_db_parameter_group.main -target=aws_iam_role_policy.execution_secrets \
     -target=aws_vpc_security_group_ingress_rule.spicedb -target=aws_vpc_security_group_ingress_rule.database_spicedb \
     -target=aws_ecs_service.spicedb -target=aws_ecs_task_definition.spicedb_migrate \
     -target=aws_ecs_task_definition.spicedb_schema \
     -target=aws_cloudwatch_event_target.spicedb -target=aws_sqs_queue_policy.spicedb)
   ```

4. **Reboot the database, in a quiet window.** `track_commit_timestamp` only
   takes effect at a restart. The instance is single-AZ, so the API and every
   worker lose the database for a minute or two; they reconnect on their own.

   ```sh
   aws rds describe-db-instances --db-instance-identifier oryxa-production \
     --query 'DBInstances[0].DBParameterGroups'          # pending-reboot
   aws rds reboot-db-instance --db-instance-identifier oryxa-production
   aws rds wait db-instance-available --db-instance-identifier oryxa-production
   deploy/scripts/sql.sh "SHOW track_commit_timestamp"   # on
   ```

5. **Migrate.** The `database` container creates the `spicedb` login and
   database if they are missing (as the oryxa login, with the password from the
   datastore URI), then `migrate` runs `spicedb datastore migrate head`. Both
   must exit 0. It is safe to run again, and is the step to repeat after moving
   to a newer SpiceDB image.

   ```sh
   run_once oryxa-production-spicedb-migrate
   ```

6. **Start the service, then write the schema, then point oryxa at it.** Two
   applies, so the oryxa tasks never ask a SpiceDB that has no schema:

   ```sh
   echo '{"spicedb_enabled": true, "spicedb_desired_count": 2}' | jq . > deploy/production/spicedb.auto.tfvars.json
   (cd deploy/production && tofu apply -target=aws_ecs_service.spicedb -target=aws_cloudwatch_metric_alarm.spicedb_running)
   aws ecs wait services-stable --cluster "$cluster" --services spicedb
   run_once oryxa-production-spicedb-schema      # zed schema write: internal/access/spicedb/schema.zed
   (cd deploy/production && tofu apply \
     -target=aws_iam_role_policy.task -target=aws_ecs_task_definition.oryxa -target=aws_ecs_service.oryxa \
     -target='aws_cloudwatch_metric_alarm.queue_age["spicedb"]' -target='aws_cloudwatch_metric_alarm.dead_letters["spicedb"]' \
     -target=aws_cloudwatch_metric_alarm.spicedb_reconcile_fixes)
   ```

   Then backfill once, rather than wait for 03:00 UTC: the projector only
   follows changes from here on. Its output, every relationship it wrote, goes
   to your terminal, not the log group, so it does not trip the fixes alarm:

   ```sh
   aws ecs execute-command --cluster "$cluster" --container oryxa --interactive \
     --task "$(aws ecs list-tasks --cluster "$cluster" --service-name oryxa --query 'taskArns[0]' --output text)" \
     --command "/oryxa spicedb-reconcile"
   ```

   The schema task carries `schema.zed` as the checkout had it at the apply;
   when the schema changes, apply that task definition and run it again.

7. **Check it.** Each task's health check is the gRPC health service
   (`grpc_health_probe` in SpiceDB's image), and Cloud Map lists a task only
   while it is healthy:

   ```sh
   aws ecs describe-services --cluster "$cluster" --services spicedb \
     --query 'services[0].[runningCount,desiredCount]'
   aws ecs describe-tasks --cluster "$cluster" --query 'tasks[].healthStatus' \
     --tasks $(aws ecs list-tasks --cluster "$cluster" --service-name spicedb --query 'taskArns' --output text)
   aws servicediscovery discover-instances --namespace-name oryxa-production.internal \
     --service-name spicedb --query 'Instances[].[Attributes.AWS_INSTANCE_IPV4,HealthStatus]'
   ```

   Two HEALTHY tasks, and the alarm `oryxa-production-spicedb-running` in OK.
   The oryxa task's logs show the shadow's disagreements, if any. After the
   first night, `oryxa-production-spicedb-reconcile-fixes` should stay OK: each
   fix it counts is an event the projector missed, named in a WARN line
   starting `spicedb reconcile:` in `/oryxa-production/app`.

8. **Turn it off again.** To pause, set `spicedb_desired_count` back to 0 and
   apply the oryxa task's targets of step 6, then the SpiceDB service's, so
   the oryxa task loses the variables before the tasks stop. To remove it, set
   `spicedb_enabled` to false and run a plain `tofu plan`: it should destroy
   SpiceDB's resources and change only the parameter group, the execution
   role's policy and the oryxa task. Apply it, adding a `-target` for each of
   those if anything else is pending (a targeted destroy does not follow
   dependencies, so name every SpiceDB resource the plan lists). The secrets go
   at once (no recovery window), and `track_commit_timestamp` returns to off at
   the next reboot. The database and login stay until dropped:

   ```sh
   deploy/scripts/sql.sh "DROP DATABASE spicedb"
   deploy/scripts/sql.sh "DROP ROLE spicedb"
   ```

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
  That log group and `/upgrade` beside it keep 30 days, as the task's do.
- **Cloudflare's rules on the zone** (`production/hardening.tf`): on `api.`,
  POSTs to exactly `/v1/apps/slack/events` and `/v1/apps/slack/interactivity`
  skip the managed WAF phase, including the Cloudflare Managed Ruleset and
  OWASP Core Ruleset. Slack's form-encoded button payloads can trip OWASP's
  anomaly scoring before they reach the origin. Apps verifies Slack's signature
  and timestamp before processing either delivery (`apps/internal/slackbot`).
  The exception is logged and uses Cloudflare provider v5 object attributes:
  `action_parameters = { phases = ["http_request_firewall_managed"] }` and
  `logging = { enabled = true }`. Other hosts, methods and paths keep managed
  WAF inspection; custom rules and rate limiting still run for Slack deliveries.
  `app.` and `api.` block every path under `/v1/internal`, and a client IP that
  POSTs to `/v1/auth/password/` more than `signin_requests_per_period` (20)
  times in a minute, counted at each Cloudflare data center, is blocked for ten minutes.
  The zone is on the Enterprise plan, which allows the window, the block and
  matching on the method. Blocked requests are in the zone's Security events,
  and a blocked one gets Cloudflare's own 429 page.
- **Alarms** mail `alarm_email`: no running task, CPU or memory high, database
  CPU, storage, memory or connections, a queue backing up, anything in a
  dead-letter queue, the connector instance failing its checks, three or more
  application errors in 15 minutes, an SES complaint or a few bounces in an
  hour, EventBridge giving up on a delivery (including the forwarding of SES's
  events), and Bedrock throttling. The three about mail are in ap-south-1,
  beside SES; the rest are in us-west-2.

A shell in the running task: `aws ecs execute-command --cluster
oryxa-production --task <id> --container oryxa --interactive --command sh`
(the image is distroless, so only `/oryxa` is there). The database from a
laptop, through the connector instance:

```sh
aws ssm start-session --target <instance id> \
  --document-name AWS-StartPortForwardingSessionToRemoteHost \
  --parameters host=<database address>,portNumber=5432,localPortNumber=15432
```

## Recovering a deleted file

**A deleted object in the files, artifacts or knowledge-base documents bucket
stays recoverable for 30 days.** The three buckets are versioned
(`production/hardening.tf`): what the task deletes only gets a delete marker
laid over its newest version, an overwrite keeps the version it replaced, and
S3 removes those older versions 30 days after they stopped being the newest.
The task role cannot delete a version itself. An object a lifecycle rule
expires (a file after 90 days, an artifact after 30) is kept for 30 more days
after that, the same way. The rule that removes the older versions reaches the
files and artifacts buckets with `provision-app.sh`, so until that has run
their older versions are kept without limit.

To bring a deleted object back, find its delete marker and remove it, which
makes the newest real version current again; to bring an overwritten one back,
copy the older version over it:

```sh
aws s3api list-object-versions --bucket oryxa-production-files-<account> --prefix <key>
aws s3api delete-object --bucket oryxa-production-files-<account> --key <key> \
  --version-id <the delete marker's VersionId>
aws s3api copy-object --bucket oryxa-production-files-<account> --key <key> \
  --copy-source "oryxa-production-files-<account>/<key>?versionId=<an older VersionId>"
```

That restores the bytes only. What names the object in PostgreSQL, such as the
row files keeps for each file, is not brought back with it. Once the 30 days
have passed the older versions are gone. The S3 Vectors buckets are not
versioned.

## At launch

- Aurora Serverless v2 in place of the t4g.small instance: snapshot, restore
  into a cluster, point the `database-url` secret and Hyperdrive at it.
- A second task and Multi-AZ.
- A database user for Hyperdrive with only what Workers read, rather than the
  owner.
- Infrastructure from CI too: `tofu plan` on pull requests and `tofu apply`
  behind the same approval, which needs a broader role than releases do.
