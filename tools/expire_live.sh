#!/bin/sh
set -eu

root="${ARKISM_ROOT:-/srv/arkism}"
export MEMORY_API_TOKEN="$(cat "$root/config/worker.token")"
# The scope the host tools use (arkism.env); "default" only when none is set.
common="--workspace-id ${ARKISM_WORKSPACE:-default} --project-id ${ARKISM_PROJECT:-default} --user-id ${ARKISM_USER:-management:owner} --retention-days 14 --batch-size 50 --max-batches 2 --execute"

# shellcheck disable=SC2086
"$root/bin/arkism" expire-candidates $common
# shellcheck disable=SC2086
"$root/bin/arkism" expire-candidates $common --agent-id agent_server_smoke
