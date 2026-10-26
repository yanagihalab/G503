#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOME_DIR="${G503_HOME:-/private/tmp/g503-e2e-demo}"
export G503_RPC_STATE_DIR="$HOME_DIR/operator-rpc-state"
CHAIN_ID="${G503_CHAIN_ID:-g503-local-1}"
RPC_LADDR="${G503_RPC_LADDR:-tcp://127.0.0.1:26657}"
RPC_URL="${G503_RPC_URL:-http://127.0.0.1:26657}"
P2P_LADDR="${G503_P2P_LADDR:-tcp://127.0.0.1:26656}"
API_ADDRESS="${G503_API_ADDRESS:-tcp://127.0.0.1:1317}"
API_URL="${G503_API_URL:-http://127.0.0.1:1317}"
GRPC_ADDRESS="${G503_GRPC_ADDRESS:-127.0.0.1:9090}"
RISK_LISTEN="${G503_RISK_LISTEN:-127.0.0.1:8081}"
RISK_URL="${G503_RISK_URL:-http://$RISK_LISTEN}"
LLM_LISTEN="${G503_LLM_LISTEN:-127.0.0.1:8082}"
LLM_URL="${G503_LLM_URL:-http://$LLM_LISTEN}"
AGENT_API_LISTEN="${G503_AGENT_API_LISTEN:-127.0.0.1:8083}"
AGENT_API_URL="${G503_AGENT_API_URL:-http://$AGENT_API_LISTEN}"
AGENT_API_TOKEN="${G503_AGENT_API_TOKEN:-local-agent-token}"
GOCACHE_DIR="${GOCACHE:-$ROOT_DIR/../../.gocache}"
LOG_DIR="${G503_LOG_DIR:-$HOME_DIR/logs}"
BIN_DIR="$HOME_DIR/bin"

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 1
fi
if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required" >&2
  exit 1
fi
if ! command -v xxd >/dev/null 2>&1; then
  echo "xxd is required" >&2
  exit 1
fi

wait_http() {
  local url="$1"
  local attempts="$2"
  local allow_method_error="${3:-false}"
  for _ in $(seq 1 "$attempts"); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    if [[ "$allow_method_error" == "true" ]]; then
      local status
      status="$(curl -s -o /dev/null -w '%{http_code}' "$url" || true)"
      if [[ "$status" == "405" || "$status" == "400" ]]; then
        return 0
      fi
    fi
    sleep 1
  done
  echo "timed out waiting for $url" >&2
  return 1
}

wait_active_sanction() {
  local api_url="$1"
  local hash="$2"
  local output="$3"
  local attempts="$4"
  for _ in $(seq 1 "$attempts"); do
    if curl -fsS "$api_url/sanction/v1/tx/$hash/active" >"$output" 2>/dev/null; then
      if jq -e '.sanction.tx_hash' "$output" >/dev/null 2>&1; then
        return 0
      fi
    fi
    sleep 1
  done
  echo "timed out waiting for active sanction $hash" >&2
  return 1
}

query_account_field() {
  local api_url="$1"
  local address="$2"
  local field="$3"
  curl -fsS "$api_url/cosmos/auth/v1beta1/accounts/$address" | jq -r --arg field "$field" '
    .account as $account |
    if $field == "account_number" then
      ($account.account_number // $account.base_account.account_number // $account.value.account_number // $account.value.base_account.account_number)
    else
      ($account.sequence // $account.base_account.sequence // $account.value.sequence // $account.value.base_account.sequence)
    end
  '
}

cd "$ROOT_DIR"

pids=()
cleanup() {
  local exit_code=$?
  if [[ "$exit_code" != "0" && "${G503_DEBUG_HOLD_ON_FAIL:-}" != "" ]]; then
    sleep "$G503_DEBUG_HOLD_ON_FAIL"
  fi
  for pid in "${pids[@]:-}"; do
    if kill -0 "$pid" >/dev/null 2>&1; then
      kill "$pid" >/dev/null 2>&1 || true
    fi
  done
  for pid in "${pids[@]:-}"; do wait "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT

echo "[1/9] init local chain: $HOME_DIR"
rm -rf "$HOME_DIR"
mkdir -p "$LOG_DIR" "$BIN_DIR"
GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/g503d" ./cmd/g503d
GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/risk-service" ./dev/mock/risk-service
GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/llm-service" ./dev/mock/llm-service
GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/agent-api" ./dev/mock/agent-api
GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/sanction-agent" ./dev/agent/cmd/sanction-agent
"$BIN_DIR/g503d" init demo --chain-id "$CHAIN_ID" --home "$HOME_DIR"
perl -0pi -e 's/^timeout_commit = "[^"]*"/timeout_commit = "3s"/m' "$HOME_DIR/config/config.toml"

keys_file="$HOME_DIR/config/demo-keys.json"
faucet_addr="$(jq -r '.faucet.address' "$keys_file")"
faucet_priv="$(jq -r '.faucet.private_key_hex' "$keys_file")"
demo_addr="$(jq -r '.demo.address' "$keys_file")"
validator_addr="$(jq -r '.validator.address' "$keys_file")"
validator_priv="$(jq -r '.validator.private_key_hex' "$keys_file")"
agent_priv="$(jq -r '.agent.private_key_hex' "$keys_file")"

echo "[2/9] start g503d"
"$BIN_DIR/g503d" start \
  --home "$HOME_DIR" \
  --minimum-gas-prices 0stake \
  --rpc.laddr "$RPC_LADDR" \
  --p2p.laddr "$P2P_LADDR" \
  --api.enable \
  --api.address "$API_ADDRESS" \
  --grpc.address "$GRPC_ADDRESS" \
  >"$LOG_DIR/g503d.log" 2>&1 &
pids+=("$!")
wait_http "$RPC_URL/status" 60
wait_http "$API_URL/cosmos/base/tendermint/v1beta1/node_info" 60
faucet_account="$(query_account_field "$API_URL" "$faucet_addr" account_number)"
faucet_sequence="$(query_account_field "$API_URL" "$faucet_addr" sequence)"
validator_account="$(query_account_field "$API_URL" "$validator_addr" account_number)"
validator_sequence="$(query_account_field "$API_URL" "$validator_addr" sequence)"
agent_addr="$(jq -r '.agent.address' "$keys_file")"
agent_account="$(query_account_field "$API_URL" "$agent_addr" account_number)"
agent_sequence="$(query_account_field "$API_URL" "$agent_addr" sequence)"

echo "[3/9] start mock risk and mock local LLM"
"$BIN_DIR/risk-service" \
  -listen "$RISK_LISTEN" \
  -high-risk-addresses "$demo_addr" \
  >"$LOG_DIR/risk.log" 2>&1 &
risk_pid="$!"
pids+=("$risk_pid")
wait_http "$RISK_URL/v1/address/$demo_addr/risk" 30
"$BIN_DIR/llm-service" \
  -listen "$LLM_LISTEN" \
  >"$LOG_DIR/llm.log" 2>&1 &
pids+=("$!")
wait_http "$LLM_URL/v1/explain" 30 true
"$BIN_DIR/agent-api" -listen "$AGENT_API_LISTEN" -api-token "$AGENT_API_TOKEN" >"$LOG_DIR/agent-api.log" 2>&1 &
pids+=("$!")
wait_http "$AGENT_API_URL/healthz" 30

echo "[4/9] broadcast normal bank.MsgSend and check mempool"
FROM_KEY=validator TO_KEY=faucet SEQUENCE="$validator_sequence" AMOUNT=1stake \
  G503_HOME="$HOME_DIR" G503_RPC_URL="$RPC_URL" G503_API_URL="$API_URL" \
  ./dev/scripts/send-demo-transfer.sh | tee "$LOG_DIR/normal-send.json"

echo "[4b/9] verify watch recovery and two automatic signed sanction transactions"
normal_hash="$(jq -r '.tx_hash' "$LOG_DIR/normal-send.json")"
normal_committed=false
for _ in $(seq 1 30); do
  if curl -fsS "$RPC_URL/tx?hash=0x$normal_hash" 2>/dev/null | jq -e '.result.tx_result.code == 0' >/dev/null; then normal_committed=true; break; fi
  sleep 1
done
if [[ "$normal_committed" != "true" ]]; then echo "normal transfer was not committed" >&2; exit 1; fi
kill "$risk_pid"
wait "$risk_pid" 2>/dev/null || true
"$BIN_DIR/sanction-agent" -watch -broadcast \
  -rpc-url "$RPC_URL" -chain-id "$CHAIN_ID" -agent-id validator-1 \
  -privkey-hex "$agent_priv" -account-number "$agent_account" -sequence "$agent_sequence" \
  -risk-url "$RISK_URL" -llm-url "$LLM_URL" -agent-api-url "$AGENT_API_URL" \
  -agent-api-token "$AGENT_API_TOKEN" -poll-interval 25ms -gas 500000 -fee 0stake \
  >"$LOG_DIR/watch-broadcast.jsonl" 2>"$LOG_DIR/watch-broadcast.err" &
watch_pid="$!"
pids+=("$watch_pid")
for i in 1 2; do
  validator_sequence="$(query_account_field "$API_URL" "$validator_addr" sequence)"
  GOCACHE="$GOCACHE_DIR" go run ./dev/agent/cmd/g503-send \
    -rpc-url "$RPC_URL" -chain-id "$CHAIN_ID" -from "$validator_addr" -to "$demo_addr" \
    -privkey-hex "$validator_priv" -account-number "$validator_account" -sequence "$validator_sequence" \
    -amount "${i}stake" -fee 0stake >"$LOG_DIR/watch-send$i.json"
  if [[ "$i" == "1" ]]; then
    retry_seen=false
    for _ in $(seq 1 20); do
      if grep -q 'watch retry:' "$LOG_DIR/watch-broadcast.err"; then retry_seen=true; break; fi
      sleep 0.05
    done
    if [[ "$retry_seen" != "true" ]]; then echo "watch did not detect the risk service outage" >&2; exit 1; fi
    "$BIN_DIR/risk-service" -listen "$RISK_LISTEN" -high-risk-addresses "$demo_addr" >"$LOG_DIR/risk-restarted.log" 2>&1 &
    pids+=("$!")
    wait_http "$RISK_URL/v1/address/$demo_addr/risk" 30
  fi
  hash="$(jq -r '.tx_hash' "$LOG_DIR/watch-send$i.json")"
  wait_active_sanction "$API_URL" "$hash" "$LOG_DIR/watch-active$i.json" 60
done
kill "$watch_pid"
wait "$watch_pid" 2>/dev/null || true
jq -s -e '[.[] | select(.broadcast.result.code == 0)] | length == 2' "$LOG_DIR/watch-broadcast.jsonl" >/dev/null
agent_sequence="$(query_account_field "$API_URL" "$agent_addr" sequence)"
jq -n --arg sequence "$agent_sequence" '{risk_service_retry:true,automatic_sanctions:2,agent_sequence_after_watch:$sequence}' >"$LOG_DIR/watch-metrics.json"

echo "[5/9] build suspicious signed bank.MsgSend without broadcasting"
dry_json="$LOG_DIR/suspicious-dry-run.json"
GOCACHE="$GOCACHE_DIR" go run ./dev/agent/cmd/g503-send \
  -dry-run \
  -rpc-url "$RPC_URL" \
  -chain-id "$CHAIN_ID" \
  -from "$faucet_addr" \
  -to "$demo_addr" \
  -amount 10stake \
  -privkey-hex "$faucet_priv" \
  -account-number "$faucet_account" \
  -sequence "$faucet_sequence" \
  -fee 0stake \
  >"$dry_json"
tx_hash="$(jq -r '.tx_hash' "$dry_json")"
tx_bytes_hex="$(jq -r '.tx_bytes_hex' "$dry_json")"

tx_meta="$LOG_DIR/suspicious-transfer.json"
jq -n \
  --arg tx_hash "$tx_hash" \
  --arg sender "$faucet_addr" \
  --arg recipient "$demo_addr" \
  '{tx_hash:$tx_hash,sender:$sender,recipient:$recipient,amount:"10",denom:"stake"}' \
  >"$tx_meta"

echo "[6/9] agent detects high risk and broadcasts signed sanction tx"
GOCACHE="$GOCACHE_DIR" go run ./dev/agent/cmd/sanction-agent \
  -tx "$tx_meta" \
  -risk-url "$RISK_URL" \
  -llm-url "$LLM_URL" \
  -agent-api-url "$AGENT_API_URL" \
  -agent-api-token "$AGENT_API_TOKEN" \
  -rpc-url "$RPC_URL" \
  -chain-id "$CHAIN_ID" \
  -agent-id validator-1 \
  -privkey-hex "$agent_priv" \
  -account-number "$agent_account" \
  -sequence "$agent_sequence" \
  -gas 500000 \
  -fee 0stake \
  -broadcast \
  -execute \
  >"$LOG_DIR/agent-sanction.json"

echo "[7/9] query active sanction state"
active_json="$LOG_DIR/active-sanction.json"
wait_active_sanction "$API_URL" "$tx_hash" "$active_json" 60
jq -e '.decision.action == "block_tx" and .broadcast.result.code == 0 and (.risk_report.evidence_hash | length > 0) and (.vote.rationale_hash | length > 0)' "$LOG_DIR/agent-sanction.json" >/dev/null
curl -fsS "$API_URL/sanction/v1/tx/$tx_hash/risk_reports" >"$LOG_DIR/on-chain-reports.json"
jq -e --slurpfile local "$LOG_DIR/agent-sanction.json" '.reports[0].evidence_hash == $local[0].risk_report.evidence_hash and .reports[0].llm_rationale_hash == $local[0].vote.rationale_hash' "$LOG_DIR/on-chain-reports.json" >/dev/null
jq '.sanction.case_id, .sanction.action' "$active_json"

echo "[8/9] broadcast the already-sanctioned tx bytes"
broadcast_json="$LOG_DIR/suppressed-broadcast.json"
tx_base64="$(printf '%s' "$tx_bytes_hex" | xxd -r -p | base64 | tr -d '\n')"
jq -n --arg tx "$tx_base64" \
  '{jsonrpc:"2.0",id:1,method:"broadcast_tx_sync",params:{tx:$tx}}' \
  | curl -fsS -H 'Content-Type: application/json' --data-binary @- "$RPC_URL" \
  >"$broadcast_json"
jq -e '.result.code == 0' "$broadcast_json" >/dev/null
sleep 4

query_json="$LOG_DIR/suppressed-query.json"
curl -s "$RPC_URL/tx?hash=0x$tx_hash" >"$query_json" || true
if jq -e '.result.tx_result.code == 0' "$query_json" >/dev/null 2>&1; then
  echo "sanctioned tx was committed unexpectedly" >&2
  exit 1
fi
echo "sanctioned tx was not committed after proposal processing"

echo "[9/9] run proposal hook unit confirmation"
GOCACHE="$GOCACHE_DIR" go test ./app -run TestSanctionAppProposalHandlersFilterActiveSanctions -count=1

echo "E2E demo completed"
echo "logs: $LOG_DIR"
