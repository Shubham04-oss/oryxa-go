# Stack test

k6 is the client. It walks one organization through teams, files, and email the way an owner, org admin, team member, org outsider, and stranger would: create and hide teams, store personal, team, organization, and billing files, call the three AI tools, and flip the mail switch. Team-call mail lands in Mailpit.

`test/cmd/stack` seeds the organization, starts the auth stub, and runs the real teams, files, and email services in one `oryxa` process (`ORYXA_SERVICES=teams,files,email`). Auth is a stub because real auth is Cognito. Local email goes over SMTP to Mailpit. Local objects go to LocalStack S3.

```sh
brew install k6
./test/run.sh
```

Mailpit UI: [http://localhost:8025](http://localhost:8025)

`run.sh` starts Postgres, LocalStack, Moto SES, and Mailpit if needed, boots the auth stub on `:18080` and oryxa on `:18083`, which serves both teams and files, and runs `test/k6/stack.js`. Tokens are the role names (`owner`, `admin`, `member`, `outsider`, `stranger`).
