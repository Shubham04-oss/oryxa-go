# Workspace CLI: first implementation

This is the first executable slice of [the onboarding design](design/onboarding.md).
It reconciles organizations, invitations, member roles, teams, and team
membership through the existing auth and teams APIs. It does not yet install
Slack, configure agents or search, or store the other settings in the design.
Those sections are rejected before an apply makes any change.

Build the standalone command from the repository root:

```sh
mkdir -p bin
go build -o bin/oryxa ./cmd/oryxa-cli
```

Create a starting file and inspect it:

```sh
./bin/oryxa init --org acme --domains acme.com -o oryxa.yaml
./bin/oryxa validate -f oryxa.yaml
```

After filling in members and teams, run the one-process setup flow. It opens
Cognito in a browser, receives the redirect on `127.0.0.1`, shows the plan,
and asks before applying. The access token stays in that process's memory.
The server must have `COGNITO_CLI_CLIENT_ID` and its fixed callback URL from
the Cognito provisioner. `--no-browser` prints the URL for a coding agent to
open; `--json` prints machine-readable progress.

```sh
./bin/oryxa setup -f oryxa.yaml
```

Separate remote commands read a short-lived member access token from
`ORYXA_ACCESS_TOKEN` in their environment. No token is accepted in the file
or as a flag, and the CLI does not save it. The standalone `login` and
cross-command in-memory session from the design are still to build.

```sh
./bin/oryxa plan -f oryxa.yaml --json
./bin/oryxa apply -f oryxa.yaml --json
./bin/oryxa apply -f oryxa.yaml --json  # resume after invited people join
./bin/oryxa status --org acme --json
./bin/oryxa pull --org acme -o current-oryxa.yaml
```

`--api` defaults to `https://api.oryxa.in`; local development may point it
at `http://localhost:8080`. `init` and `pull` write YAML to stdout unless
`-o` is supplied. They refuse to overwrite an existing file without `--yes`.
`plan`, `apply`, and `diff` read `oryxa.yaml` by default. `--dry-run` on apply
prints the plan. JSON output on plan and apply contains `changes` and
`needs_human`; apply exits `2` when a person must accept an invitation before
their team membership can be added.

The current file accepts these settings:

| Setting | Effect |
| --- | --- |
| `metadata.organization` | Organization slug; creates the organization if missing |
| `metadata.domains` | Local validation guard for member emails; not yet stored in auth |
| `organization.name` | Organization display name |
| `people.members[].email`, `.role` | Invite members and change existing non-owner roles |
| `people.invite: email` | Uses the existing invitation email flow |
| `teams[].name`, `.members` | Create teams and add joined members |

The parser refuses unknown keys, duplicate members or teams, unrecognized
roles, and unsupported sections. Omitting a person or team never removes it.
`people.prune`, quiet invitations, and owner role transfers are not yet
supported. `preset` is accepted in the file so the file shape matches the
design; preset policy settings do not take effect yet. The command does not
currently lock concurrent applies or version files on the server.
