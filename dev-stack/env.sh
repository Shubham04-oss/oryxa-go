# Sourced by up.sh, down.sh and smoke.sh: the stack's settings, exported.
# .env.example first (the repository's defaults), then .env.dev, then your
# own untracked .env.local. .env.local is read before .env.dev too, so the
# ports it sets reach every URL .env.dev builds from them.
cd "$(dirname "${BASH_SOURCE[0]}")/.."
set -a
own=dev-stack/.env.local
[ -f $own ] || own=/dev/null
. ./.env.example
. $own
. dev-stack/.env.dev
. $own
set +a
# A profile would point the AWS CLI and SDK at real credentials.
unset AWS_PROFILE
RUN=$PWD/dev-stack/.run
mkdir -p "$RUN"
