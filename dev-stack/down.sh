#!/usr/bin/env bash
# Stop the oryxa process and auth stub up.sh started. The compose
# containers stay up (other stacks may share them): `docker compose stop`.
set -euo pipefail
. "$(dirname "$0")/env.sh"
for name in oryxa authstub; do
  if [ -f "$RUN/$name.pid" ]; then
    kill "$(cat "$RUN/$name.pid")" 2>/dev/null && [ "${1:-}" != quiet ] && echo "stopped $name"
    rm -f "$RUN/$name.pid"
  fi
done
true
