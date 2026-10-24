# The self-hosted runners

CI (`ci.yml`) runs on self-hosted runners labelled `self-hosted, oryxa-go`, except for a pull request from a fork, which runs on GitHub's `ubuntu-latest`. The production deploy (`deploy.yml`) always runs on GitHub's machines (`ubuntu-24.04-arm` builds the arm64 image natively), never here: it holds the AWS deploy role, the Cloudflare token and the deploy key that bypasses main's ruleset.

- **Linux (a1, or a host of its own):** `linux.sh` below. This is how runners should be set up.
- **The Mac:** `start.sh`, further down. It predates the hardening and gives jobs the Mac's Docker socket; use it only for a private repository.

## Linux runners

```bash
sudo install -d -m 700 /etc/oryxa-runner
sudo install -m 600 /dev/stdin /etc/oryxa-runner/token   # paste the token, then Ctrl-D
sudo deploy/runner/linux.sh oryxa-go-1 oryxa-go-2
```

- **The token** is a fine-grained personal access token for this repository only, with Administration: write and nothing else. It registers one runner per job; jobs never see it.
- **One job per runner, then a clean slate** (`ephemeral.sh`). Each job gets a just-in-time registration that GitHub removes after it. Before the next, everything the runner's user runs is killed and its home, work directory, tool cache and Docker data are deleted. Nothing a job plants reaches a later one; the cost is cold caches, so every job downloads its Go modules, pnpm store and images again.
- **No root.** Each runner runs as `runner-NAME`: no sudo, not in the `docker` group (which is root on the host). Jobs get Docker from a rootless engine running as that user, so `docker run`, service containers and `docker build` work as before.
- **A gate on the runner** (`job-started.sh`, the runner's job-started hook). It fails a job before any of its code runs if the job is for another repository, a pull request from a fork, `pull_request_target`, or `deploy.yml`. A pull request can edit `ci.yml`, so the routing there is not enough on its own. `job-started-test.sh` checks the gate.
- **Network.** A runner's user (its containers included) reaches the internet and the host's loopback only. Private ranges, the tailnet except its DNS, and the cloud's metadata service are refused.
- **Updating:** run `linux.sh` again. It fetches the latest runner, verifies its checksum and restarts the runners, cancelling running jobs.
- **Removing one:** `sudo systemctl disable --now oryxa-runner@NAME`, then `sudo userdel runner-NAME` and `sudo rm /etc/oryxa-runner/NAME.env`. GitHub removes just-in-time runners itself.

`linux.sh` prints a warning for every other home or runner directory a runner's user can still read. Fix each one (`chmod 700`), or better, give the runners a host of their own.

## The Mac runner

**Start it, or bring it back** after a restart or an image change:

```bash
deploy/runner/start.sh
```

- It builds `oryxa-runner:latest`: GitHub's runner image plus gh and the AWS CLI, which the deploy uses, and `libatomic1` for Node 26.
- It registers the runner on the first start. That needs `gh` signed in as a repository admin.
- It runs it with `--restart unless-stopped`, so it comes back whenever Docker Desktop does.
- Its registration, work directory and tool cache live in the Docker volume `oryxa-mac-1`.
- A second runner runs two jobs at once: `RUNNER_NAME=oryxa-mac-2 deploy/runner/start.sh`. See "The second runner" below.

**What it needs from the Mac**
- **Docker Desktop running, and the Mac awake.** A sleeping Mac holds jobs in the queue until it wakes. Deploys then wait with them.
- **Host networking:** CI's PostgreSQL service answers on `localhost:5432`, as on GitHub's machines.
- **The Docker socket,** because jobs start service containers and build images.

**The second runner** (`oryxa-mac-2`, since 2026-09-30) gets its own Docker engine, `oryxa-mac-2-docker`, running Docker in Docker. The runner joins that engine's network.
- **Why:** CI's PostgreSQL service takes port 5432 on the network it runs on. Two jobs on one engine would fight over it, as would any test that listens on a fixed port. On the second runner, services answer on its own localhost.
- **Caps:** Docker Desktop has 7.75 GB, and the first runner has peaked at 5.2 GB. So the second runner is capped at 4 GB and its engine at 3 GB, and it builds and tests 4 Go packages at a time (`GOFLAGS=-p=4`). A busy second runner can't starve the first, which deploys. Raising Docker Desktop's memory (Settings → Resources, when no job is running) gives both more room.
- **Its first jobs are slower:** its engine and Go caches start empty.
- **The engine's trust:** it runs privileged, which amounts to the same access as the first runner's Docker socket.

**Trust.** A job here runs as root in a container holding the Mac's Docker socket, so it can reach everything on the Mac. That is acceptable only while just the owner and their agents can run code on it: a private repository, or the Linux runners' gate above. On a public repository use `linux.sh` instead.

**Removing it:**
1. Run `docker rm -f oryxa-mac-1 && docker volume rm oryxa-mac-1`.
2. For the second runner, run `docker rm -f oryxa-mac-2 oryxa-mac-2-docker && docker volume rm oryxa-mac-2 oryxa-mac-2-docker`.
3. Delete each runner under the repository's Settings → Actions → Runners.
