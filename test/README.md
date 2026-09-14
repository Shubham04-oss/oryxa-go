# Stack test

k6 is the client. It walks one organization through teams, files, integrations, and email the way an owner, org admin, team member, org outsider, and stranger would: create and hide teams, store personal, team, organization, and billing files, call the three AI tools, flip the mail switch, register Slack, complete OAuth, run an action, admit a webhook, and disconnect. Team-call mail lands in Mailpit.

`test/cmd/stack` seeds the organization and starts the auth stub, the real teams, files, and email binaries, and `integrations/cmd/stack`. Slack, KMS, and the piece runner are the same stand-ins the integrations e2e already uses. Auth is a stub because real auth is Cognito. Local email goes over SMTP to Mailpit. Local objects go to LocalStack S3.

```sh
brew install k6
./test/run.sh
```

Mailpit UI: [http://localhost:8025](http://localhost:8025)

`run.sh` starts Postgres, LocalStack, Moto SES, and Mailpit if needed, boots the harness on `:18080` / `:18083` / `:18084` / `:18085`, and runs `test/k6/stack.js`. Tokens are the role names (`owner`, `admin`, `member`, `outsider`, `stranger`).
