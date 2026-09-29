# The self-hosted runner

CI and CD run on a GitHub Actions runner in Docker on the owner's Mac, not on GitHub's machines. GitHub's minutes for this private repository cost money, and the account's budget ran out on 2026-09-30. The Mac is Apple silicon, so the images a release pushes are built natively for Fargate's arm64.

**Where jobs run.** The repository variable `CI_RUNNER` decides:
- `["self-hosted","oryxa-mac"]` sends every job in `ci.yml` and `deploy.yml` to this runner;
- deleting the variable sends them back to GitHub's `ubuntu-latest`.

```bash
gh variable set CI_RUNNER --body '["self-hosted","oryxa-mac"]'   # this runner
gh variable delete CI_RUNNER                                        # GitHub's machines
```

**Start it, or bring it back** after a restart or an image change:

```bash
deploy/runner/start.sh
```

- It builds `oryxa-runner:latest`: GitHub's runner image plus gh and the AWS CLI, which the deploy uses.
- It registers the runner on the first start. That needs `gh` signed in as a repository admin.
- It runs it with `--restart unless-stopped`, so it comes back whenever Docker Desktop does.
- Its registration, work directory and tool cache live in the Docker volume `oryxa-mac-1`.
- A second runner runs two jobs at once: `RUNNER_NAME=oryxa-mac-2 deploy/runner/start.sh`.

**What it needs from the Mac**
- **Docker Desktop running, and the Mac awake.** A sleeping Mac holds jobs in the queue until it wakes. Deploys then wait with them.
- **Host networking:** CI's PostgreSQL service answers on `localhost:5432`, as on GitHub's machines.
- **The Docker socket,** because jobs start service containers and build images.

**Trust.** A job can reach the Mac's files through Docker's shared folders. That's acceptable because only the owner and their agents push to this private repository. **Never attach this runner to a public repository,** since there anyone's pull request could run code on the Mac.

**Removing it:** `docker rm -f oryxa-mac-1 && docker volume rm oryxa-mac-1`, and delete the runner under the repository's Settings → Actions → Runners.
