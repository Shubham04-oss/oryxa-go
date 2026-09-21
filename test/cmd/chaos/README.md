# chaos

Throws fake and hostile payloads at a locally running copy of the whole Oryxa
stack (all nine services in one process) and watches what happens. It is for
finding what breaks, not for regression testing: nothing here is a product
service and none of it may be deployed.

```sh
go build -o /tmp/chaos ./test/cmd/chaos

/tmp/chaos rig up                # boot an isolated stack and hold it (Ctrl-C tears it down)
/tmp/chaos fuzz -v               # every operation, then hostile variants of each, then tenant checks
/tmp/chaos attack                # list the targeted experiments
/tmp/chaos attack preauth-body search-cost mail-amplification
/tmp/chaos rig clean --id <id>   # remove what a rig that died left behind
```

Run `rig up` in one terminal and the rest in another. `--workdir` (default
`$TMPDIR/oryxa-chaos`) is how they find each other.

## What is isolated

A run touches nothing you develop against. `rig up` gives itself:

- its own PostgreSQL **database** (`chaos_<id>`) on the shared server, dropped on exit;
- its own event bus, queues, bucket and DynamoDB tables on the shared LocalStack,
  all named `c<id>-…`, removed on exit by prefix only;
- an **auth stub** instead of Cognito, so no account is ever created in the
  staging pool. It is driven live (`/_stub/*`) and can misbehave on demand;
- fake **Composio** and **Bedrock** servers, so the apps and rag services run
  without keys;
- an **egress guard**: the process runs with `HTTPS_PROXY` pointing at a proxy
  that refuses everything and writes down what was asked for, so a call to
  Composio, Dodo, Google or real AWS fails at once instead of leaving the
  machine. `HOME` is empty and the AWS config files are `/dev/null`.

`test/cmd/oryxa-chaos` is `cmd/oryxa` plus a loopback pprof listener, so goroutine
and heap profiles can be read while it is abused. Keep its service list in step
with `cmd/oryxa/services.go`.

## What it does

- **fuzz** sends each operation's working request once (a smoke test of every
  route), then hundreds of variants: about 110 hostile JSON fragments dropped
  into every field (NUL, invalid UTF-8, huge numbers, deep nesting, type
  confusion, injection strings), path, query and header variants, framing and
  size, every method, and about twenty ways of lying about who you are.
  Consuming operations (deletes) run last. Method and route variants that could
  change data are aimed at ids that do not exist.
- **isolation**: every operation with credentials that are valid for something,
  aimed at the other organization's objects; and a role matrix.
- **judging**: a 5xx, a hang, a dropped connection, internals in a body, or a
  2xx to something that must be refused. Findings are folded by route and
  class, and each carries a `curl` and the server's `X-Request-Id`.
- **monitoring** every two seconds: RSS, goroutines and heap (pprof), open
  file descriptors, database connections and lock waits, queue and dead-letter
  depth, mail sent, auth lookups, and the Docker disk. After the run it prints
  the server's own error log, digested, and its access log per route.

## Rails

- `rig up` **refuses to start** with under 3 GiB free on the Docker disk
  (`--min-free-gb`), and a run **stops** if it falls under 1 GiB. Colima's VM
  has its own small disk; a full one stops every container on it, including the
  development database. Check the VM's disk, not the Mac's.
- Cleanup removes only what carries the run's prefix, and reports what it
  could not remove instead of hiding it.

## Not covered

The real `auth` service (it needs Cognito; a fake OIDC issuer plus a fake
Cognito API would be the way in), agent runs (the streaming routes need a
model), SQS event injection, and what production's IAM policies allow. The
apps, knowledge, billing and agents catalogs are shallow: routes, ids and
framing, not valid bodies.
