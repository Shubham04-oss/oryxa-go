#!/bin/sh
# The sandbox container's entrypoint: keep the two code kernels running as
# the agent user (a kernel exits when a run outlasts its timeout, and is
# started fresh), and stay up for SandboxDO's exec calls.
set -u
mkdir -p /tmp/oryxa && chown agent:agent /tmp/oryxa

supervise() {
  while true; do
    runuser -u agent -- "$@" >>/tmp/oryxa/kernel.log 2>&1
    sleep 0.2
  done
}

supervise python3 /opt/oryxa/kernel.py &
supervise node /opt/oryxa/kernel.mjs &
exec sleep infinity
