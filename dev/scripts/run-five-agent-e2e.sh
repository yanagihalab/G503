#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export G503_AUTONOMOUS_E2E=1
export G503_MULTI_HOME="${G503_MULTI_HOME:-${TMPDIR:-/tmp}/g503-five-agent-e2e}"
exec bash "$ROOT_DIR/dev/scripts/run-five-validator-e2e.sh"
