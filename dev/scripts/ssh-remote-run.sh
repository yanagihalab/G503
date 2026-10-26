#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

EXPERIMENT="${G503_SSH_EXPERIMENT:-agent-flow}"
RESULT_ROOT="${G503_SSH_RESULT_ROOT:-$PWD/ssh-results}"
RUN_ID="${G503_SSH_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
RUN_DIR="$RESULT_ROOT/$RUN_ID"
GOCACHE_DIR="${GOCACHE:-$PWD/.gocache}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --experiment)
      EXPERIMENT="$2"
      shift 2
      ;;
    --result-root)
      RESULT_ROOT="$2"
      RUN_DIR="$RESULT_ROOT/$RUN_ID"
      shift 2
      ;;
    --run-id)
      RUN_ID="$2"
      RUN_DIR="$RESULT_ROOT/$RUN_ID"
      shift 2
      ;;
    -h|--help)
      cat <<'USAGE'
Usage: bash dev/scripts/ssh-remote-run.sh [--experiment NAME]

Experiments:
  checks               bash syntax + Go tests, standard vet and race tests
  agent-flow           checks + mock risk/LLM/agent-api sanction-agent integration
  local-e2e            checks + single-node local E2E
  five-validator       checks + five-validator sanction E2E
  five-agent           checks + five durable autonomous agents with crash recovery
  sanction-safety      checks + five-agent recovery, settlement safety and reviewed revoke
  agent-recovery       checks + sanction-safety, expired evidence, failed retry and backup restore
  validator-lifecycle  checks + validator add/jail/unbond lifecycle E2E
  ownership-snapshot   checks + five-agent ownership failover and fixed voting through validator churn
  all                  checks + agent-flow + local-e2e + five-validator + agent-recovery + validator-lifecycle + ownership-snapshot
USAGE
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      exit 2
      ;;
  esac
done

case "$EXPERIMENT" in
  checks|agent-flow|local-e2e|five-validator|five-agent|sanction-safety|agent-recovery|ownership-snapshot|validator-lifecycle|all) ;;
  *) echo "unknown experiment: $EXPERIMENT" >&2; exit 2 ;;
esac
if [[ ! "$RUN_ID" =~ ^[A-Za-z0-9_-]+$ ]]; then
  echo "run-id must contain only letters, digits, underscores, or hyphens" >&2
  exit 2
fi
mkdir -p "$RESULT_ROOT" "$GOCACHE_DIR"
RESULT_ROOT="$(cd "$RESULT_ROOT" && pwd)"
RUN_DIR="$RESULT_ROOT/$RUN_ID"
mkdir "$RUN_DIR"
mkdir "$RUN_DIR/logs"
ln -sfn "$RUN_DIR" "$RESULT_ROOT/latest"
CURRENT_STEP="setup"
finish_run() {
  local exit_code=$?
  local status="passed"
  if [[ "$exit_code" != "0" ]]; then status="failed"; fi
  jq -n --arg run_id "$RUN_ID" --arg experiment "$EXPERIMENT" \
    --arg status "$status" --arg step "$CURRENT_STEP" --argjson exit_code "$exit_code" \
    --arg finished_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg result_dir "$RUN_DIR" \
    '{run_id:$run_id,experiment:$experiment,status:$status,step:$step,exit_code:$exit_code,finished_at:$finished_at,result_dir:$result_dir}' \
    >"$RUN_DIR/summary.json"
  cat "$RUN_DIR/summary.json"
  if [[ "$exit_code" != "0" ]]; then
    tail -n 40 "$RUN_DIR/logs/$CURRENT_STEP.log" >&2 2>/dev/null || true
  fi
}
trap finish_run EXIT

run_step() {
  local name="$1"
  shift
  CURRENT_STEP="$name"
  echo "[$(date -u +%H:%M:%S)] $name"
  "$@" >"$RUN_DIR/logs/$name.log" 2>&1
}

write_meta() {
  jq -n --arg run_id "$RUN_ID" --arg experiment "$EXPERIMENT" \
    --arg started_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg host "$(hostname)" \
    --arg repo "$PWD" --arg go_version "$(go env GOVERSION)" --arg result_dir "$RUN_DIR" \
    '{run_id:$run_id,experiment:$experiment,started_at:$started_at,host:$host,repo:$repo,go_version:$go_version,result_dir:$result_dir}' \
    >"$RUN_DIR/metadata.json"
}

run_checks() {
  run_step "bash-syntax" bash -c 'for script in dev/scripts/*.sh dev/scripts/tests/*.sh; do bash -n "$script" || exit; done'
  run_step "go-test" env "GOCACHE=$GOCACHE_DIR" go test -count=1 ./...
  run_step "go-vet" env "GOCACHE=$GOCACHE_DIR" go vet ./...
  run_step "go-race" env "GOCACHE=$GOCACHE_DIR" go test -race -count=1 ./...
}

run_agent_flow() {
  run_step "agent-flow" env \
    "GOCACHE=$GOCACHE_DIR" \
    "G503_AGENT_FLOW_EVAL_DIR=$RUN_DIR/agent-flow" \
    "G503_RISK_LISTEN=127.0.0.1:59281" \
    "G503_LLM_LISTEN=127.0.0.1:59282" \
    "G503_AGENT_API_LISTEN=127.0.0.1:59283" \
    bash dev/scripts/evaluate-agent-flow.sh
}

run_local_e2e() {
  run_step "local-e2e" env \
    "GOCACHE=$GOCACHE_DIR" \
    "G503_HOME=$RUN_DIR/local-e2e" \
    "G503_RISK_LISTEN=127.0.0.1:59291" \
    "G503_LLM_LISTEN=127.0.0.1:59292" \
    "G503_AGENT_API_LISTEN=127.0.0.1:59293" \
    "G503_RPC_LADDR=tcp://127.0.0.1:59157" \
    "G503_RPC_URL=http://127.0.0.1:59157" \
    "G503_P2P_LADDR=tcp://127.0.0.1:59156" \
    "G503_API_ADDRESS=tcp://127.0.0.1:59117" \
    "G503_API_URL=http://127.0.0.1:59117" \
    "G503_GRPC_ADDRESS=127.0.0.1:59190" \
    bash dev/scripts/run-local-e2e-demo.sh
}

run_five_validator() {
  run_step "five-validator" env \
    "GOCACHE=$GOCACHE_DIR" \
    "G503_MULTI_HOME=$RUN_DIR/five-validator" \
    "G503_BASE_RPC_PORT=59357" \
    "G503_BASE_P2P_PORT=59456" \
    "G503_BASE_API_PORT=59517" \
    "G503_BASE_GRPC_PORT=59690" \
    "G503_RISK_BLOCK_LISTEN=127.0.0.1:59201" \
    "G503_RISK_REVERT_LISTEN=127.0.0.1:59202" \
    "G503_LLM_LISTEN=127.0.0.1:59203" \
    bash dev/scripts/run-five-validator-e2e.sh
}

run_validator_lifecycle() {
  run_step "validator-lifecycle" env \
    "GOCACHE=$GOCACHE_DIR" \
    "G503_LIFECYCLE_HOME=$RUN_DIR/validator-lifecycle" \
    "G503_BASE_RPC_PORT=59757" \
    "G503_BASE_P2P_PORT=59856" \
    "G503_BASE_API_PORT=59917" \
    "G503_BASE_GRPC_PORT=60090" \
    bash dev/scripts/run-validator-lifecycle-e2e.sh
}

run_five_agent() {
  local mode="${1:-five-agent}"
  local script="dev/scripts/run-five-agent-e2e.sh"
  if [[ "$mode" == "sanction-safety" ]]; then script="dev/scripts/run-sanction-safety-e2e.sh"; fi
  if [[ "$mode" == "agent-recovery" ]]; then script="dev/scripts/run-agent-recovery-e2e.sh"; fi
  run_step "$mode" env \
    "GOCACHE=$GOCACHE_DIR" \
    "G503_MULTI_HOME=$RUN_DIR/$mode" \
    "G503_BASE_RPC_PORT=60357" "G503_BASE_P2P_PORT=60456" \
    "G503_BASE_API_PORT=60517" "G503_BASE_GRPC_PORT=60690" \
    "G503_RISK_BLOCK_LISTEN=127.0.0.1:60201" \
    "G503_RISK_REVERT_LISTEN=127.0.0.1:60202" "G503_LLM_LISTEN=127.0.0.1:60203" \
    "G503_AUTO_API_LISTEN=127.0.0.1:60204" "G503_AUTO_PROXY_LISTEN=127.0.0.1:60205" \
    bash "$script"
}

write_meta
run_checks

case "$EXPERIMENT" in
  checks)
    ;;
  agent-flow)
    run_agent_flow
    ;;
  local-e2e)
    run_local_e2e
    ;;
  five-validator)
    run_five_validator
    ;;
  five-agent)
    run_five_agent
    ;;
  sanction-safety)
    run_five_agent sanction-safety
    ;;
  agent-recovery)
    run_five_agent agent-recovery
    ;;
  validator-lifecycle)
    run_validator_lifecycle
    ;;
  ownership-snapshot)
    run_step ownership-snapshot env "GOCACHE=$GOCACHE_DIR" "G503_LIFECYCLE_HOME=$RUN_DIR/ownership-snapshot" bash dev/scripts/run-ownership-snapshot-e2e.sh
    ;;
  all)
    run_agent_flow
    run_local_e2e
    run_five_validator
    run_five_agent agent-recovery
    run_validator_lifecycle
    run_step ownership-snapshot env "GOCACHE=$GOCACHE_DIR" "G503_LIFECYCLE_HOME=$RUN_DIR/ownership-snapshot" bash dev/scripts/run-ownership-snapshot-e2e.sh
    ;;
  *)
    echo "unknown experiment: $EXPERIMENT" >&2
    exit 2
    ;;
esac
