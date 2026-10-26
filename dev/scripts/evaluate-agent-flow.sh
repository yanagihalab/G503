#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOG_DIR="${G503_AGENT_FLOW_EVAL_DIR:-/private/tmp/g503-agent-flow-eval}"
RISK_LISTEN="${G503_RISK_LISTEN:-127.0.0.1:58281}"
RISK_URL="${G503_RISK_URL:-http://$RISK_LISTEN}"
LLM_LISTEN="${G503_LLM_LISTEN:-127.0.0.1:58282}"
LLM_URL="${G503_LLM_URL:-http://$LLM_LISTEN}"
AGENT_API_LISTEN="${G503_AGENT_API_LISTEN:-127.0.0.1:58283}"
AGENT_API_URL="${G503_AGENT_API_URL:-http://$AGENT_API_LISTEN}"
AGENT_API_TOKEN="${G503_AGENT_API_TOKEN:-local-agent-token}"
AGENT_API_ROLE="${G503_AGENT_API_ROLE:-validator-agent}"
GOCACHE_DIR="${GOCACHE:-$ROOT/../../.gocache}"

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 1
fi
if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required" >&2
  exit 1
fi

mkdir -p "$LOG_DIR"
cd "$ROOT"

pids=()
BIN_DIR="$LOG_DIR/bin"
mkdir -p "$BIN_DIR"
cleanup() {
  for pid in "${pids[@]:-}"; do
    if kill -0 "$pid" >/dev/null 2>&1; then
      kill "$pid" >/dev/null 2>&1 || true
    fi
  done
  for pid in "${pids[@]:-}"; do wait "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT

wait_http() {
  local url="$1"
  for _ in $(seq 1 30); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for $url" >&2
  return 1
}

wait_http_method_ready() {
  local url="$1"
  for _ in $(seq 1 30); do
    local status
    status="$(curl -s -o /dev/null -w '%{http_code}' "$url" || true)"
    if [[ "$status" == "200" || "$status" == "400" || "$status" == "405" ]]; then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for $url" >&2
  return 1
}

GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/risk-service" ./dev/mock/risk-service
GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/llm-service" ./dev/mock/llm-service
GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/agent-api" ./dev/mock/agent-api
"$BIN_DIR/risk-service" \
  -listen "$RISK_LISTEN" \
  -high-risk-addresses g5031scamrecipient \
  -high-risk-score 86 \
  -high-risk-categories scam \
  >"$LOG_DIR/risk.log" 2>&1 &
pids+=("$!")
wait_http "$RISK_URL/v1/address/g5031scamrecipient/risk"

"$BIN_DIR/llm-service" \
  -listen "$LLM_LISTEN" \
  >"$LOG_DIR/llm.log" 2>&1 &
pids+=("$!")
wait_http_method_ready "$LLM_URL/v1/explain"

"$BIN_DIR/agent-api" \
  -listen "$AGENT_API_LISTEN" \
  -api-token "$AGENT_API_TOKEN" \
  >"$LOG_DIR/agent-api.log" 2>&1 &
pids+=("$!")
wait_http "$AGENT_API_URL/healthz"

jq -n '{
  tx_hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  sender: "g5031sender",
  recipient: "g5031scamrecipient",
  amount: "100",
  denom: "stake",
  confirmed: false
}' >"$LOG_DIR/tx.json"
key_dir="$(mktemp -d "$LOG_DIR/keys.XXXXXX")"
GOCACHE="$GOCACHE_DIR" go run ./dev/agent/cmd/g503-ops rotate-key -out "$key_dir/agent.hex" -agent-id validator-1 >"$LOG_DIR/test-key-info.json"

GOCACHE="$GOCACHE_DIR" go run ./dev/agent/cmd/sanction-agent \
  -tx "$LOG_DIR/tx.json" \
  -risk-url "$RISK_URL" \
  -llm-url "$LLM_URL" \
  -agent-api-url "$AGENT_API_URL" \
  -agent-api-token "$AGENT_API_TOKEN" \
  -agent-api-role "$AGENT_API_ROLE" \
  -agent-id validator-1 \
  -key-file "$key_dir/agent.hex" \
  -chain-id g503-agent-flow-test \
  >"$LOG_DIR/sanction-agent-output.json"

jq -e '
  .decision.decision_id != null and
  .decision.calibrated_score >= .decision.raw_risk_score and
  .action == "block_tx" and
  .risk.risk_score == .decision.calibrated_score and
  (.risk_report.submitter_signature | length > 0) and
  (.vote.signature | length > 0) and
  .risk_report.llm_rationale_hash == .vote.rationale_hash
' "$LOG_DIR/sanction-agent-output.json" >/dev/null

jq -n \
  --slurpfile output "$LOG_DIR/sanction-agent-output.json" \
  --arg logs "$LOG_DIR" \
  '{
    passed: true,
    action: $output[0].action,
    raw_risk_score: $output[0].decision.raw_risk_score,
    calibrated_score: $output[0].decision.calibrated_score,
    decision_id: $output[0].decision.decision_id,
    logs: $logs
  }' | tee "$LOG_DIR/summary.json"
