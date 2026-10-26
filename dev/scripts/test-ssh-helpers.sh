#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/g503-ssh-test.XXXXXX")"
trap 'rm -rf "$TEMP_DIR"' EXIT
mkdir -p "$TEMP_DIR/bin"
for command in go ssh rsync; do
  cp "$ROOT/dev/scripts/tests/fake-command.sh" "$TEMP_DIR/bin/$command"
  chmod +x "$TEMP_DIR/bin/$command"
done
export PATH="$TEMP_DIR/bin:$PATH"
export G503_TEST_CALL_LOG="$TEMP_DIR/calls.log"

# Old Go must fail before running tests or downloading a toolchain.
if G503_TEST_GO_VERSION=1.24.9 bash "$ROOT/dev/scripts/ssh-remote-setup.sh" >"$TEMP_DIR/version.log" 2>&1; then
  echo "setup accepted a Go version older than go.mod" >&2; exit 1
fi
grep -q '1.25.10' "$TEMP_DIR/version.log"

# A failed remote test must still produce a machine-readable failure summary.
if G503_TEST_GO_EXIT=9 bash "$ROOT/dev/scripts/ssh-remote-run.sh" --experiment checks --result-root "$TEMP_DIR/results" --run-id failure >"$TEMP_DIR/run.log" 2>&1; then
  echo "failed checks incorrectly passed" >&2; exit 1
fi
jq -e '.status == "failed" and .exit_code == 9 and .step == "go-test"' "$TEMP_DIR/results/failure/summary.json" >/dev/null

# The local wrapper must attempt result collection even when SSH execution fails.
status=0
bash "$ROOT/dev/scripts/ssh-server-experiment.sh" --host example.test --user tester --local-results "$TEMP_DIR/fetched" >"$TEMP_DIR/wrapper.log" 2>&1 || status=$?
[[ "$status" == "7" ]]
[[ "$(grep -c '^rsync ' "$G503_TEST_CALL_LOG")" == "2" ]]
echo "SSH helper regression tests passed"
