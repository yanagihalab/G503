#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOME_DIR="${G503_MULTI_HOME:-/private/tmp/g503-five-validator-e2e}"
CHAIN_ID="${G503_CHAIN_ID:-g503-five-validators-1}"
BASE_RPC_PORT="${G503_BASE_RPC_PORT:-42657}"
BASE_P2P_PORT="${G503_BASE_P2P_PORT:-44656}"
BASE_API_PORT="${G503_BASE_API_PORT:-15317}"
BASE_GRPC_PORT="${G503_BASE_GRPC_PORT:-42090}"
RISK_BLOCK_LISTEN="${G503_RISK_BLOCK_LISTEN:-127.0.0.1:59081}"
RISK_BLOCK_URL="${G503_RISK_BLOCK_URL:-http://$RISK_BLOCK_LISTEN}"
RISK_REVERT_LISTEN="${G503_RISK_REVERT_LISTEN:-127.0.0.1:59082}"
RISK_REVERT_URL="${G503_RISK_REVERT_URL:-http://$RISK_REVERT_LISTEN}"
LLM_LISTEN="${G503_LLM_LISTEN:-127.0.0.1:59083}"
LLM_URL="${G503_LLM_URL:-http://$LLM_LISTEN}"
GOCACHE_DIR="${GOCACHE:-$ROOT_DIR/../../.gocache}"
LOG_DIR="$HOME_DIR/logs"
METRICS_JSON="$LOG_DIR/metrics.json"
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

cd "$ROOT_DIR"

pids=()
node_pids=()
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
  local attempts="$2"
  for _ in $(seq 1 "$attempts"); do
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
  local attempts="$2"
  for _ in $(seq 1 "$attempts"); do
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

wait_tx_committed() {
  local rpc_url="$1"
  local hash="$2"
  local attempts="$3"
  for _ in $(seq 1 "$attempts"); do
    if curl -fsS "$rpc_url/tx?hash=0x$hash" | jq -e '.result.tx_result.code == 0' >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for tx $hash" >&2
  return 1
}

wait_json_field() {
  local url="$1"
  local jq_expr="$2"
  local output="$3"
  local attempts="$4"
  for _ in $(seq 1 "$attempts"); do
    if curl -fsS "$url" >"$output" 2>/dev/null && jq -e "$jq_expr" "$output" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for $url" >&2
  return 1
}

run_agent_tx() {
  local node_index="$1"
  shift
  local keys_file="$HOME_DIR/node$node_index/config/demo-keys.json"
  local agent_addr agent_priv account sequence
  agent_addr="$(jq -r '.agent.address' "$keys_file")"
  agent_priv="$(jq -r '.agent.private_key_hex' "$keys_file")"
  account="$(query_account_field "$API_URL" "$agent_addr" account_number)"
  sequence="$(query_account_field "$API_URL" "$agent_addr" sequence)"
  "$BIN_DIR/sanction-agent" \
    -rpc-url "$RPC_URL" \
    -chain-id "$CHAIN_ID" \
    -agent-id "validator-$node_index" \
    -privkey-hex "$agent_priv" \
    -account-number "$account" \
    -sequence "$sequence" \
    -gas 700000 \
    -fee 0stake \
    "$@"
}

start_node() {
  local node_index="$1"
  local log_file="$2"
  local peer_list=""
  for peer_index in 1 2 3 4 5; do
    if [[ "$node_index" == "$peer_index" ]]; then
      continue
    fi
    if [[ -n "$peer_list" ]]; then
      peer_list+=","
    fi
    peer_list+="${peers[$((peer_index - 1))]}"
  done
  local rpc_port="$((BASE_RPC_PORT + node_index - 1))"
  local p2p_port="$((BASE_P2P_PORT + node_index - 1))"
  local api_port="$((BASE_API_PORT + node_index - 1))"
  local grpc_port="$((BASE_GRPC_PORT + node_index - 1))"
  "$BIN_DIR/g503d" start \
    --home "$HOME_DIR/node$node_index" \
    --chain-id "$CHAIN_ID" \
    --minimum-gas-prices 0stake \
    --rpc.laddr "tcp://127.0.0.1:$rpc_port" \
    --p2p.laddr "tcp://127.0.0.1:$p2p_port" \
    --p2p.persistent_peers "$peer_list" \
    --api.enable \
    --api.address "tcp://127.0.0.1:$api_port" \
    --grpc.address "127.0.0.1:$grpc_port" \
    >"$log_file" 2>&1 &
  pids+=("$!")
  node_pids+=("$!")
}

echo "[1/12] initialize five validator homes: $HOME_DIR"
rm -rf "$HOME_DIR"
mkdir -p "$LOG_DIR" "$BIN_DIR"
GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/g503d" ./cmd/g503d
GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/sanction-agent" ./dev/agent/cmd/sanction-agent
GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/risk-service" ./dev/mock/risk-service
GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/llm-service" ./dev/mock/llm-service
genesis_files=()
node_ids=()
for i in 1 2 3 4 5; do
  home="$HOME_DIR/node$i"
  "$BIN_DIR/g503d" init "node$i" --chain-id "$CHAIN_ID" --home "$home" >"$LOG_DIR/init-node$i.log"
  perl -0pi -e 's/addr_book_strict = true/addr_book_strict = false/; s/allow_duplicate_ip = false/allow_duplicate_ip = true/' "$home/config/config.toml"
  genesis_files+=("$home/config/genesis.json")
  node_ids+=("$("$BIN_DIR/g503d" comet show-node-id --home "$home")")
done

echo "[2/12] compose shared genesis with five validators and five agents"
jq -s '
  .[0] as $base |
  ([.[].validators[0]]) as $validators |
  ([range(0; length) as $i |
    .[$i].app_state.sanction.agents[0]
    | .agent_id = ("validator-" + (($i + 1) | tostring))
    | .metadata_uri = "local-five-node"
  ]) as $agents |
  ([.[].app_state.auth.accounts[] | select(."@type" == "/cosmos.auth.v1beta1.ModuleAccount")] | unique_by(.base_account.address)) as $modules |
  ([.[].app_state.auth.accounts[] | select(."@type" == "/cosmos.auth.v1beta1.BaseAccount")] | unique_by(.address) | to_entries | map(.value.account_number = ((.key + 10) | tostring) | .value.sequence = "0" | .value)) as $users |
  ([.[].app_state.bank.balances[]] | unique_by(.address)) as $balances |
  $base
  | .validators = $validators
  | .app_state.auth.accounts = ($modules + $users)
  | .app_state.bank.balances = $balances
  | .app_state.bank.supply = [{"denom":"stake","amount": (($balances | length * 1000000000) | tostring)}]
  | .app_state.sanction.agents = $agents
' "${genesis_files[@]}" >"$HOME_DIR/genesis.combined.json"
for i in 1 2 3 4 5; do
  cp "$HOME_DIR/genesis.combined.json" "$HOME_DIR/node$i/config/genesis.json"
done

peers=()
for i in 1 2 3 4 5; do
  p2p_port="$((BASE_P2P_PORT + i - 1))"
  peers+=("${node_ids[$((i - 1))]}@127.0.0.1:$p2p_port")
done

RPC_URL="http://127.0.0.1:$BASE_RPC_PORT"
API_URL="http://127.0.0.1:$BASE_API_PORT"

echo "[3/12] start five g503d validators"
for i in 1 2 3 4 5; do
  start_node "$i" "$LOG_DIR/node$i.log"
done
wait_http "$RPC_URL/status" 90
wait_http "$API_URL/sanction/v1/params" 90

validator_count="$(curl -fsS "$RPC_URL/validators" | jq -r '.result.validators | length')"
if [[ "$validator_count" != "5" ]]; then
  echo "expected 5 validators, got $validator_count" >&2
  exit 1
fi
echo "confirmed validator count: $validator_count"

echo "[4/12] start risk services and central local LLM"
demo_addr="$(jq -r '.demo.address' "$HOME_DIR/node1/config/demo-keys.json")"
"$BIN_DIR/risk-service" \
  -listen "$RISK_BLOCK_LISTEN" \
  -high-risk-addresses "$demo_addr" \
  -high-risk-score 86 \
  -high-risk-categories scam \
  >"$LOG_DIR/risk-block.log" 2>&1 &
pids+=("$!")
wait_http "$RISK_BLOCK_URL/v1/address/$demo_addr/risk" 30
"$BIN_DIR/risk-service" \
  -listen "$RISK_REVERT_LISTEN" \
  -high-risk-addresses "$demo_addr" \
  -high-risk-score 95 \
  -high-risk-categories money_laundering \
  >"$LOG_DIR/risk-revert.log" 2>&1 &
pids+=("$!")
wait_http "$RISK_REVERT_URL/v1/address/$demo_addr/risk" 30
"$BIN_DIR/llm-service" \
  -listen "$LLM_LISTEN" \
  >"$LOG_DIR/llm.log" 2>&1 &
pids+=("$!")
wait_http_method_ready "$LLM_URL/v1/explain" 30

if [[ "${G503_AUTONOMOUS_E2E:-0}" == "1" ]]; then
  source "$ROOT_DIR/dev/scripts/tests/five-agent-scenario.sh"
  if [[ "${G503_RECOVERY_E2E:-0}" == "1" ]]; then source "$ROOT_DIR/dev/scripts/tests/agent-recovery-scenario.sh"; fi
  run_five_agent_scenario
  if [[ "${G503_SAFETY_E2E:-0}" == "1" ]]; then
    source "$ROOT_DIR/dev/scripts/tests/sanction-safety-scenario.sh"
    run_sanction_safety_scenario
  fi
  if [[ "${G503_RECOVERY_E2E:-0}" == "1" ]]; then run_agent_recovery_scenario; fi
  exit 0
fi

echo "[5/12] broadcast normal transfer"
FROM_KEY=validator TO_KEY=faucet AMOUNT=1stake \
  G503_HOME="$HOME_DIR/node1" G503_RPC_URL="$RPC_URL" G503_API_URL="$API_URL" \
  ./dev/scripts/send-demo-transfer.sh | tee "$LOG_DIR/normal-send.json"

echo "[6/12] commit suspicious transfer for revert scenario"
faucet_addr="$(jq -r '.faucet.address' "$HOME_DIR/node1/config/demo-keys.json")"
faucet_priv="$(jq -r '.faucet.private_key_hex' "$HOME_DIR/node1/config/demo-keys.json")"
faucet_account="$(query_account_field "$API_URL" "$faucet_addr" account_number)"
faucet_sequence="$(query_account_field "$API_URL" "$faucet_addr" sequence)"
revert_send="$LOG_DIR/revert-send.json"
GOCACHE="$GOCACHE_DIR" go run ./dev/agent/cmd/g503-send \
  -rpc-url "$RPC_URL" \
  -chain-id "$CHAIN_ID" \
  -from "$faucet_addr" \
  -to "$demo_addr" \
  -amount 10stake \
  -privkey-hex "$faucet_priv" \
  -account-number "$faucet_account" \
  -sequence "$faucet_sequence" \
  -fee 0stake \
  >"$revert_send"
revert_tx_hash="$(jq -r '.tx_hash' "$revert_send")"
wait_tx_committed "$RPC_URL" "$revert_tx_hash" 60
jq -n \
  --arg tx_hash "$revert_tx_hash" \
  --arg sender "$faucet_addr" \
  --arg recipient "$demo_addr" \
  '{tx_hash:$tx_hash,sender:$sender,recipient:$recipient,amount:"10",denom:"stake"}' \
  >"$LOG_DIR/revert-transfer.json"

echo "[7/12] five agents approve and execute confirmed-transfer revert"
start_ns="$(date +%s%N)"
run_agent_tx 1 \
  -tx "$LOG_DIR/revert-transfer.json" \
  -risk-url "$RISK_REVERT_URL" \
  -llm-url "$LLM_URL" \
  -target-type address \
  -broadcast \
  >"$LOG_DIR/revert-agent1-open-vote.json"
case_id="case-$revert_tx_hash"
wait_json_field "$API_URL/sanction/v1/cases/$case_id" '.sanction_case.case_id' "$LOG_DIR/revert-case.json" 60
for i in 2 3 4 5; do
  run_agent_tx "$i" \
    -vote-only \
    -case-id "$case_id" \
    -vote-action revert_transfer \
    >"$LOG_DIR/revert-agent$i-vote.json"
done
run_agent_tx 1 \
  -execute-only \
  -case-id "$case_id" \
  >"$LOG_DIR/revert-execute.json"
wait_json_field "$API_URL/sanction/v1/executions/$case_id" '.record.result_code == "ok"' "$LOG_DIR/revert-execution-record.json" 60
revert_end_ns="$(date +%s%N)"

echo "[8/12] start watch-mode agent and verify pending tx decode"
watch_log="$LOG_DIR/watch-agent.jsonl"
"$BIN_DIR/sanction-agent" -rpc-url "$RPC_URL" -agent-id validator-5 \
  -watch \
  -risk-url "$RISK_BLOCK_URL" \
  -llm-url "$LLM_URL" \
  -poll-interval 500ms \
  >"$watch_log" 2>"$LOG_DIR/watch-agent.err" &
watch_pid="$!"
pids+=("$watch_pid")
sleep 2

echo "[9/12] create block_tx case with 4 of 5 votes and verify proposal suppression"
faucet_account="$(query_account_field "$API_URL" "$faucet_addr" account_number)"
faucet_sequence="$(query_account_field "$API_URL" "$faucet_addr" sequence)"
dry_json="$LOG_DIR/block-dry-run.json"
GOCACHE="$GOCACHE_DIR" go run ./dev/agent/cmd/g503-send \
  -dry-run \
  -rpc-url "$RPC_URL" \
  -chain-id "$CHAIN_ID" \
  -from "$faucet_addr" \
  -to "$demo_addr" \
  -amount 11stake \
  -privkey-hex "$faucet_priv" \
  -account-number "$faucet_account" \
  -sequence "$faucet_sequence" \
  -fee 0stake \
  >"$dry_json"
block_tx_hash="$(jq -r '.tx_hash' "$dry_json")"
block_tx_bytes_hex="$(jq -r '.tx_bytes_hex' "$dry_json")"
jq -n \
  --arg tx_hash "$block_tx_hash" \
  --arg sender "$faucet_addr" \
  --arg recipient "$demo_addr" \
  '{tx_hash:$tx_hash,sender:$sender,recipient:$recipient,amount:"11",denom:"stake"}' \
  >"$LOG_DIR/block-transfer.json"
run_agent_tx 1 \
  -tx "$LOG_DIR/block-transfer.json" \
  -risk-url "$RISK_BLOCK_URL" \
  -llm-url "$LLM_URL" \
  -broadcast \
  >"$LOG_DIR/block-agent1-open-vote.json"
block_case_id="case-$block_tx_hash"
wait_json_field "$API_URL/sanction/v1/cases/$block_case_id" '.sanction_case.case_id' "$LOG_DIR/block-case.json" 60
for i in 2 3 4; do
  run_agent_tx "$i" \
    -vote-only \
    -case-id "$block_case_id" \
    -vote-action block_tx \
    >"$LOG_DIR/block-agent$i-vote.json"
done
wait_json_field "$API_URL/sanction/v1/tx/$block_tx_hash/active" '.sanction.tx_hash' "$LOG_DIR/block-active.json" 60
tx_base64="$(printf '%s' "$block_tx_bytes_hex" | xxd -r -p | base64 | tr -d '\n')"
jq -n --arg tx "$tx_base64" \
  '{jsonrpc:"2.0",id:1,method:"broadcast_tx_sync",params:{tx:$tx}}' \
  | curl -fsS -H 'Content-Type: application/json' --data-binary @- "http://127.0.0.1:$((BASE_RPC_PORT + 1))" \
  >"$LOG_DIR/block-suppressed-broadcast-node2.json"
sleep 5
curl -s "$RPC_URL/tx?hash=0x$block_tx_hash" >"$LOG_DIR/block-suppressed-query.json" || true
if jq -e '.result.tx_result.code == 0' "$LOG_DIR/block-suppressed-query.json" >/dev/null 2>&1; then
  echo "sanctioned tx committed unexpectedly on five-validator network" >&2
  exit 1
fi
if ! grep -qi "$block_tx_hash" "$watch_log"; then
  echo "watch-mode agent did not observe the pending suspicious transfer" >&2
  exit 1
fi

echo "[10/12] restart all nodes and verify sanction state persistence"
for pid in "${node_pids[@]:-}"; do
  kill "$pid" >/dev/null 2>&1 || true
done
for pid in "${node_pids[@]:-}"; do
  wait "$pid" 2>/dev/null || true
done
for i in 1 2 3 4 5; do
  if curl --max-time 2 -fsS "http://127.0.0.1:$((BASE_RPC_PORT + i - 1))/status" >/dev/null 2>&1; then
    echo "node$i RPC remained reachable after shutdown" >&2
    exit 1
  fi
done
sleep 3
node_pids=()
for i in 1 2 3 4 5; do
  start_node "$i" "$LOG_DIR/node$i-restart.log"
done
wait_http "$RPC_URL/status" 90
wait_http "$API_URL/sanction/v1/params" 90
for i in 1 2 3 4 5; do
  wait_http "http://127.0.0.1:$((BASE_RPC_PORT + i - 1))/status" 90
  wait_json_field "http://127.0.0.1:$((BASE_API_PORT + i - 1))/sanction/v1/tx/$block_tx_hash/active" '.sanction.tx_hash' "$LOG_DIR/node$i-active-after-restart.json" 60
done
restart_validator_count="$(curl -fsS "$RPC_URL/validators" | jq -r '.result.validators | length')"
if [[ "$restart_validator_count" != "5" ]]; then
  echo "expected 5 validators after full restart, got $restart_validator_count" >&2
  exit 1
fi
wait_json_field "$API_URL/sanction/v1/executions/$case_id" '.record.result_code == "ok"' "$LOG_DIR/revert-execution-after-restart.json" 60
wait_json_field "$API_URL/sanction/v1/tx/$block_tx_hash/active" '.sanction.tx_hash' "$LOG_DIR/block-active-after-restart.json" 60
curl -fsS "$API_URL/sanction/v1/agents" >"$LOG_DIR/query-agents-after-restart.json"
jq -e '.agents | length == 5' "$LOG_DIR/query-agents-after-restart.json" >/dev/null

echo "[11/12] query coverage smoke"
curl -fsS "$API_URL/sanction/v1/params" >"$LOG_DIR/query-params.json"
curl -fsS "$API_URL/sanction/v1/agents" >"$LOG_DIR/query-agents.json"
curl -fsS "$API_URL/sanction/v1/cases/$case_id/votes" >"$LOG_DIR/query-revert-votes.json"
curl -fsS "$API_URL/sanction/v1/tx/$revert_tx_hash/risk_reports" >"$LOG_DIR/query-revert-reports.json"
curl -fsS "$API_URL/sanction/v1/tx/$block_tx_hash/cases" >"$LOG_DIR/query-block-cases.json"
jq -e '.agents | length == 5' "$LOG_DIR/query-agents.json" >/dev/null
jq -e '.votes | length == 5' "$LOG_DIR/query-revert-votes.json" >/dev/null

echo "[12/12] write metrics"
revert_latency_ms="$(((revert_end_ns - start_ns) / 1000000))"
jq -n \
  --argjson validators "$validator_count" \
  --argjson revert_latency_ms "$revert_latency_ms" \
  --arg revert_case "$case_id" \
  --arg block_case "$block_case_id" \
  --arg logs "$LOG_DIR" \
  '{
    validators: $validators,
    revert_consensus_latency_ms: $revert_latency_ms,
    sanction_success_rate: 1,
    revert_case_id: $revert_case,
    block_case_id: $block_case,
    logs: $logs
  }' >"$METRICS_JSON"
cat "$METRICS_JSON"
echo "five-validator E2E completed"
