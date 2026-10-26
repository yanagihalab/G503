#!/usr/bin/env bash
# Sourced by the five-validator fixture after nodes and risk/LLM services are ready.

start_auto_agents() {
  local generation="$1"
  shift
  auto_pids=()
  for i in 1 2 3 4 5; do
    "$BIN_DIR/sanction-agent" -config "$HOME_DIR/agent$i.json" \
      -target-type address -poll-interval 200ms -lease-ttl-blocks 12 -lease-wait 90s "$@" \
      >"$LOG_DIR/agent$i-$generation.jsonl" 2>"$LOG_DIR/agent$i-$generation.err" &
    auto_pids+=("$!")
    pids+=("$!")
  done
  for i in 1 2 3 4 5; do
    local ready=0
    for _ in $(seq 1 90); do
      if grep -q '"event":"ready"' "$LOG_DIR/agent$i-$generation.jsonl"; then ready=1; break; fi
      sleep 1
    done
    if [[ "$ready" != "1" ]]; then
      tail -n 20 "$LOG_DIR/agent$i-$generation.err" >&2
      echo "agent$i did not start" >&2
      return 1
    fi
  done
}

stop_auto_agents() {
  local signal="$1"
  for pid in "${auto_pids[@]}"; do kill "-$signal" "$pid" 2>/dev/null || true; done
  for pid in "${auto_pids[@]}"; do wait "$pid" 2>/dev/null || true; done
  local remaining=()
  for pid in "${pids[@]}"; do
    local is_agent=0
    for agent_pid in "${auto_pids[@]}"; do if [[ "$pid" == "$agent_pid" ]]; then is_agent=1; fi; done
    if [[ "$is_agent" == "0" ]]; then remaining+=("$pid"); fi
  done
  pids=("${remaining[@]}")
  auto_pids=()
}

auto_balance() {
  curl -fsS "$API_URL/cosmos/bank/v1beta1/balances/$1/by_denom?denom=stake" | jq -r '.balance.amount'
}

auto_send_transfer() {
  local amount="$1"
  local output="$2"
  local number sequence
  number="$(query_account_field "$API_URL" "$auto_faucet" account_number)"
  sequence="$(query_account_field "$API_URL" "$auto_faucet" sequence)"
  GOCACHE="$GOCACHE_DIR" go run ./dev/agent/cmd/g503-send \
    -rpc-url "$RPC_URL" -chain-id "$CHAIN_ID" -to "$demo_addr" -amount "$amount" \
    -privkey-hex "$(jq -r '.faucet.private_key_hex' "$HOME_DIR/node1/config/demo-keys.json")" \
    -account-number "$number" -sequence "$sequence" -fee 0stake -check-mempool=false >"$output"
}

run_five_agent_scenario() {
  umask 077
  echo "[auto 1/8] build central agent API and acknowledgement fault proxy"
  GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/agent-api" ./dev/mock/agent-api
  GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/rpc-fault-proxy" ./dev/mock/rpc-fault-proxy
  local agent_listen="${G503_AUTO_API_LISTEN:-127.0.0.1:59204}"
  local proxy_listen="${G503_AUTO_PROXY_LISTEN:-127.0.0.1:59205}"
  "$BIN_DIR/agent-api" -listen "$agent_listen" -api-token five-agent-test-token \
    >"$LOG_DIR/agent-api.log" 2>&1 &
  pids+=("$!")
  wait_http "http://$agent_listen/healthz" 30
  "$BIN_DIR/rpc-fault-proxy" -listen "$proxy_listen" -upstream "$RPC_URL" -marker "$LOG_DIR/lost-ack.json" \
    >"$LOG_DIR/rpc-fault-proxy.log" 2>&1 &
  pids+=("$!")
  wait_http "http://$proxy_listen/status" 30
  export G503_AGENT_API_TOKEN=five-agent-test-token

  echo "[auto 2/8] start five durable agent processes"
  for i in 1 2 3 4 5; do
    jq -r '.agent.private_key_hex' "$HOME_DIR/node$i/config/demo-keys.json" >"$HOME_DIR/agent$i.key"
    local agent_rpc="http://127.0.0.1:$((BASE_RPC_PORT+i-1))"
    if [[ "$i" == "1" ]]; then agent_rpc="http://$proxy_listen"; fi
    jq -n --arg chain "$CHAIN_ID" --arg rpc "$agent_rpc" --arg risk "$RISK_REVERT_URL" \
      --arg llm "$LLM_URL" --arg api "http://$agent_listen" --arg id "validator-$i" \
      --arg key "$HOME_DIR/agent$i.key" --arg state "$HOME_DIR/agent$i-state" \
      '{chain_id:$chain,rpc_url:$rpc,risk_provider:"mock",risk_url:$risk,llm_provider:"mock",llm_url:$llm,
        agent_id:$id,key_file:$key,agent_api:{url:$api,token_env:"G503_AGENT_API_TOKEN",role:"validator-agent"},
        broadcast:{gas:700000,fee:"1stake"},daemon:{enabled:true,state_dir:$state}}' >"$HOME_DIR/agent$i.json"
  done
  start_auto_agents first
  auto_faucet="$(jq -r '.faucet.address' "$HOME_DIR/node1/config/demo-keys.json")"
  local initial_faucet initial_demo
  initial_faucet="$(auto_balance "$auto_faucet")"
  initial_demo="$(auto_balance "$demo_addr")"

  echo "[auto 3/8] send suspicious transfer; agents independently detect, open and vote"
  auto_send_transfer 10stake "$LOG_DIR/auto-transfer1.json"
  local hash1 case1
  hash1="$(jq -r '.tx_hash' "$LOG_DIR/auto-transfer1.json")"
  case1="auto-$hash1"
  local injected=0
  for _ in $(seq 1 180); do
    if [[ -s "$LOG_DIR/lost-ack.json" ]]; then injected=1; break; fi
    sleep 1
  done
  if [[ "$injected" != "1" ]]; then
    for i in 1 2 3 4 5; do tail -n 12 "$LOG_DIR/agent$i-first.jsonl" >&2; done
    echo "automatic execution did not reach lost-ack injection" >&2
    return 1
  fi

  echo "[auto 4/8] SIGKILL all five agents while execute acknowledgement is withheld"
  stop_auto_agents KILL
  if [[ "${G503_RECOVERY_E2E:-0}" == "1" ]]; then backup_all_agents lost-ack; fi
  "$BIN_DIR/sanction-agent" -daemon-status -state-dir "$HOME_DIR/agent1-state" >"$LOG_DIR/agent1-crash-journal.json"
  jq -e --arg id "$case1" '.intents | any(.[]; .kind == "execute" and .case_id == $id and .status == "pending" and (.attempts | length == 1))' "$LOG_DIR/agent1-crash-journal.json" >/dev/null
  wait_json_field "$API_URL/sanction/v1/executions/$case1" '.record.result_code == "ok"' "$LOG_DIR/auto-execution1.json" 90
  [[ "$(auto_balance "$auto_faucet")" == "$initial_faucet" ]]
  [[ "$(auto_balance "$demo_addr")" == "$initial_demo" ]]

  echo "[auto 5/8] restart same five journals and reconcile the lost acknowledgement"
  if [[ "${G503_RECOVERY_E2E:-0}" == "1" ]]; then restore_all_agents lost-ack lost-ack; fi
  start_auto_agents second
  local reconciled=0
  for _ in $(seq 1 90); do
    if grep -q '"kind":"execute".*"status":"confirmed"' "$LOG_DIR/agent1-second.jsonl"; then reconciled=1; break; fi
    sleep 1
  done
  [[ "$reconciled" == "1" ]]

  echo "[auto 6/8] process a second transfer using automatically refreshed account sequences"
  auto_send_transfer 7stake "$LOG_DIR/auto-transfer2.json"
  local hash2 case2
  hash2="$(jq -r '.tx_hash' "$LOG_DIR/auto-transfer2.json")"
  case2="auto-$hash2"
  wait_json_field "$API_URL/sanction/v1/executions/$case2" '.record.result_code == "ok"' "$LOG_DIR/auto-execution2.json" 180
  for i in 1 2 3 4 5; do
    local done=0
    for _ in $(seq 1 90); do
      if grep -q "\"case_id\":\"$case2\".*\"event\":\"complete\"" "$LOG_DIR/agent$i-second.jsonl"; then done=1; break; fi
      sleep 1
    done
    [[ "$done" == "1" ]]
  done
  sleep 3
  stop_auto_agents TERM
  for i in 1 2 3 4 5; do
    "$BIN_DIR/sanction-agent" -daemon-status -state-dir "$HOME_DIR/agent$i-state" >"$LOG_DIR/agent$i-final-journal.json"
    jq -e 'all(.intents[]; .status == "confirmed" or .status == "satisfied" or .status == "cancelled")' "$LOG_DIR/agent$i-final-journal.json" >/dev/null
  done
  jq -e --arg id "$case1" '.intents | any(.[]; .kind == "execute" and .case_id == $id and .status == "confirmed" and (.attempts | length == 1) and .attempts[0].broadcasts == 1)' "$LOG_DIR/agent1-final-journal.json" >/dev/null

  echo "[auto 7/8] verify unique cases, five votes, two execution events and exact balance restoration"
  for case_hash in "$hash1" "$hash2"; do
    local id="auto-$case_hash"
    curl -fsS "$API_URL/sanction/v1/tx/$case_hash/cases" >"$LOG_DIR/$id-cases.json"
    jq -e --arg id "$id" '.sanction_cases | length == 1 and .[0].case_id == $id' "$LOG_DIR/$id-cases.json" >/dev/null
    curl -fsS "$API_URL/sanction/v1/cases/$id/votes" >"$LOG_DIR/$id-votes.json"
    jq -e '(.votes | length == 5) and ([.votes[].agent_id] | unique | length == 5) and all(.votes[]; .option == "VOTE_OPTION_APPROVE")' "$LOG_DIR/$id-votes.json" >/dev/null
  done
  local height executions=0 count
  height="$(curl -fsS "$RPC_URL/status" | jq -r '.result.sync_info.latest_block_height')"
  for h in $(seq 1 "$height"); do
    curl -fsS "$RPC_URL/block_results?height=$h" >"$LOG_DIR/block-results-$h.json"
    count="$(jq --arg one "$case1" --arg two "$case2" '[.result.txs_results[]? | select(.code == 0) | .events[]? | select(.type == "sanction_executed") | .attributes[]? | select(.key == "case_id" and (.value == $one or .value == $two))] | length' "$LOG_DIR/block-results-$h.json")"
    executions="$((executions+count))"
  done
  [[ "$executions" == "2" ]]
  [[ "$(auto_balance "$auto_faucet")" == "$initial_faucet" ]]
  [[ "$(auto_balance "$demo_addr")" == "$initial_demo" ]]

  echo "[auto 8/8] save reproducible autonomous-run metrics"
  jq -n --argjson validators "$validator_count" --argjson executions "$executions" \
    --arg first "$case1" --arg second "$case2" --arg logs "$LOG_DIR" \
    '{validators:$validators,agents:5,automatic_cases:2,successful_executions:$executions,
      all_agents_sigkill_and_restart:true,lost_execute_ack_reconciled:true,duplicate_executions:0,
      balances_restored:true,cases:[$first,$second],logs:$logs}' >"$LOG_DIR/autonomous-metrics.json"
  cat "$LOG_DIR/autonomous-metrics.json"
}
