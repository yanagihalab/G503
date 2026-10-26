#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export G503_OWNERSHIP_E2E=1
export G503_LIFECYCLE_HOME="${G503_LIFECYCLE_HOME:-${TMPDIR:-/tmp}/g503-ownership-snapshot-e2e}"
export G503_BASE_RPC_PORT="${G503_BASE_RPC_PORT:-55657}"
exec bash "$ROOT_DIR/dev/scripts/run-validator-lifecycle-e2e.sh"
