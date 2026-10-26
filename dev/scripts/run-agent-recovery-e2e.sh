#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export G503_RECOVERY_E2E=1
export G503_MULTI_HOME="${G503_MULTI_HOME:-${TMPDIR:-/tmp}/g503-agent-recovery-e2e}"
exec bash "$ROOT_DIR/dev/scripts/run-sanction-safety-e2e.sh"
