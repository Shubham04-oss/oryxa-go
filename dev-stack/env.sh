# Sourced by up.sh, down.sh and smoke.sh: the stack's settings, exported.
# .env.example first (the repository's defaults), then .env.dev, then your
# own untracked .env.local.
cd "$(dirname "${BASH_SOURCE[0]}")/.."
set -a
. ./.env.example
. dev-stack/.env.dev
if [ -f dev-stack/.env.local ]; then . dev-stack/.env.local; fi
set +a
RUN=$PWD/dev-stack/.run
mkdir -p "$RUN"
