#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LISTEN="${AGENT_API_LISTEN:-127.0.0.1:8083}"
AGENT_API_URL="${AGENT_API_URL:-http://$LISTEN}"
LOG_DIR="${AGENT_API_EVAL_DIR:-/private/tmp/g503-agent-api-eval}"
GOCACHE_DIR="${GOCACHE:-$ROOT/../../.gocache}"
AGENT_API_TOKEN="${AGENT_API_TOKEN:-local-agent-token}"
AGENT_API_ROLE="${AGENT_API_ROLE:-validator-agent}"

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

if [[ "${AGENT_API_EXTERNAL:-}" == "" ]]; then
  GOCACHE="$GOCACHE_DIR" go build -o "$LOG_DIR/agent-api" ./dev/mock/agent-api
  "$LOG_DIR/agent-api" \
    -listen "$LISTEN" \
    -latency "${AGENT_API_LATENCY:-5ms}" \
    -api-token "$AGENT_API_TOKEN" \
    >"$LOG_DIR/agent-api.log" 2>&1 &
  pids+=("$!")
fi
wait_http "$AGENT_API_URL/healthz"

write_case() {
  local path="$1"
  local agent_id="$2"
  local tx_hash="$3"
  local score="$4"
  local categories="$5"
  local target_type="$6"
  local confirmed="${7:-false}"
  local sender="${8:-g5031sender}"
  local recipient="${9:-g5031recipient}"
  jq -n \
    --arg agent_id "$agent_id" \
    --arg tx_hash "$tx_hash" \
    --arg score "$score" \
    --arg categories "$categories" \
    --arg target_type "$target_type" \
    --argjson confirmed "$confirmed" \
    --arg sender "$sender" \
    --arg recipient "$recipient" \
    '{
      agent_id: $agent_id,
      target_type: $target_type,
      tx: {
        tx_hash: $tx_hash,
        sender: $sender,
        recipient: $recipient,
        amount: "100",
        denom: "stake",
        confirmed: $confirmed
      },
      risk: {
        source: "chainalysis-mock",
        response_id: ("risk-" + $tx_hash),
        address: $recipient,
        score: ($score | tonumber),
        categories: ($categories | split(",")),
        exposure_type: "direct"
      },
      policy: {
        watch_threshold: 60,
        block_threshold: 80,
        freeze_threshold: 85,
        revert_threshold: 90,
        high_risk_categories: ["sanctioned", "scam", "money_laundering"],
        critical_risk_categories: ["sanctioned", "terrorist_financing", "ransomware"],
        trusted_addresses: ["g5031trusted"],
        blocked_addresses: ["g5031blocked"]
      }
    }' >"$path"
}

write_case "$LOG_DIR/low.json" "validator-1" "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" 12 "low_risk" "tx"
write_case "$LOG_DIR/block.json" "validator-2" "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" 86 "scam" "tx"
write_case "$LOG_DIR/revert-address.json" "validator-3" "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" 95 "money_laundering" "address"
write_case "$LOG_DIR/revert-tx.json" "validator-4" "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd" 95 "money_laundering" "tx"
write_case "$LOG_DIR/revert-confirmed-tx.json" "validator-5" "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" 95 "money_laundering" "tx" true
write_case "$LOG_DIR/trusted-watch.json" "validator-6" "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" 72 "scam" "tx" false "g5031trusted"
write_case "$LOG_DIR/blocked-critical.json" "validator-7" "abababababababababababababababababababababababababababababababab" 58 "unknown" "tx" false "g5031sender" "g5031blocked"

evaluate_case() {
  local name="$1"
  local expected_action="$2"
  local expected_vote="$3"
  local input="$LOG_DIR/$name.json"
  local output="$LOG_DIR/$name.response.json"
  local start_ns end_ns latency_ms
  start_ns="$(date +%s%N)"
  curl -fsS -H 'Content-Type: application/json' -H "Authorization: Bearer $AGENT_API_TOKEN" -H "X-Agent-Role: $AGENT_API_ROLE" --data-binary "@$input" "$AGENT_API_URL/v1/agents/evaluate" >"$output"
  end_ns="$(date +%s%N)"
  latency_ms="$(((end_ns - start_ns) / 1000000))"
  local got_action got_vote
  got_action="$(jq -r '.action' "$output")"
  got_vote="$(jq -r '.vote_option' "$output")"
  jq -e '.calibrated_score >= 0 and .calibrated_score <= 100 and (.findings | length > 0) and (.evidence_status == "complete") and (.review_level | length > 0)' "$output" >/dev/null
  jq -n \
    --arg name "$name" \
    --arg expected_action "$expected_action" \
    --arg expected_vote "$expected_vote" \
    --arg got_action "$got_action" \
    --arg got_vote "$got_vote" \
    --argjson calibrated_score "$(jq '.calibrated_score' "$output")" \
    --arg review_level "$(jq -r '.review_level' "$output")" \
    --argjson latency_ms "$latency_ms" \
    '{
      name: $name,
      expected_action: $expected_action,
      expected_vote: $expected_vote,
      got_action: $got_action,
      got_vote: $got_vote,
      calibrated_score: $calibrated_score,
      review_level: $review_level,
      latency_ms: $latency_ms,
      passed: ($expected_action == $got_action and $expected_vote == $got_vote)
    }'
}

{
  evaluate_case low none reject
  evaluate_case block block_tx approve
  evaluate_case revert-address revert_transfer approve
  evaluate_case revert-tx block_tx approve
  evaluate_case revert-confirmed-tx block_tx approve
  evaluate_case trusted-watch watch approve
  evaluate_case blocked-critical watch approve
} | jq -s '
  {
    total: length,
    passed: map(select(.passed)) | length,
    success_rate: ((map(select(.passed)) | length) / length),
    average_latency_ms: ((map(.latency_ms) | add) / length),
    cases: .
  }
' | tee "$LOG_DIR/summary.json"
