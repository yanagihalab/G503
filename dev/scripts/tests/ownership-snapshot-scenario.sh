#!/usr/bin/env bash
# Source only after the lifecycle fixture has five bonded validators and agents.

ownership_phase() {
  G503_OWNERSHIP_PHASE="$1" GOCACHE="$GOCACHE_DIR" go test -p 2 -count=1 -v \
    ./dev/agent/e2e -run '^TestOwnershipAndSnapshotChurn$' | tee "$LOG_DIR/ownership-$1-test.log"
}

run_ownership_snapshot_scenario() {
  umask 077
  local BIN_DIR="$HOME_DIR/bin"
  mkdir -p "$BIN_DIR"
  source "$ROOT_DIR/dev/scripts/tests/five-agent-scenario.sh"
  source "$ROOT_DIR/dev/scripts/tests/agent-recovery-scenario.sh"
  export G503_SAFETY_HOME="$HOME_DIR" G503_SAFETY_RPC="$RPC_URL" G503_SAFETY_API="$API_URL" G503_CHAIN_ID="$CHAIN_ID"
  local risk="${G503_OWNERSHIP_RISK_LISTEN:-127.0.0.1:61201}"
  local llm="${G503_OWNERSHIP_LLM_LISTEN:-127.0.0.1:61202}"
  local api="${G503_OWNERSHIP_API_LISTEN:-127.0.0.1:61203}"
  local demo_addr="$(jq -r '.demo.address' "$HOME_DIR/node1/config/demo-keys.json")"
  local auto_pids=()
  echo "[ownership 1/9] start central mock services and five independent signing sessions"
  GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/sanction-agent" ./dev/agent/cmd/sanction-agent
  GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/risk-service" ./dev/mock/risk-service
  GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/llm-service" ./dev/mock/llm-service
  GOCACHE="$GOCACHE_DIR" go build -o "$BIN_DIR/agent-api" ./dev/mock/agent-api
  "$BIN_DIR/risk-service" -listen "$risk" -high-risk-addresses "$demo_addr" -high-risk-score 95 >"$LOG_DIR/ownership-risk.log" 2>&1 &
  pids+=("$!")
  "$BIN_DIR/llm-service" -listen "$llm" >"$LOG_DIR/ownership-llm.log" 2>&1 &
  pids+=("$!")
  "$BIN_DIR/agent-api" -listen "$api" -api-token ownership-test >"$LOG_DIR/ownership-api.log" 2>&1 &
  pids+=("$!")
  wait_http "http://$api/healthz" 30
  export G503_AGENT_API_TOKEN=ownership-test
  local rpc_endpoints='[]'
  for i in 1 2 3 4 5; do rpc_endpoints="$(jq --arg rpc "http://127.0.0.1:$((BASE_RPC_PORT+i-1))" '.+[$rpc]' <<<"$rpc_endpoints")"; done
  for i in 1 2 3 4 5; do
    jq -r '.validator.private_key_hex' "$HOME_DIR/node$i/config/demo-keys.json" >"$HOME_DIR/agent$i.key"
    local address="$(jq -r '.validator.address' "$HOME_DIR/node$i/config/demo-keys.json")"
    local id="$(curl -fsS "$API_URL/sanction/v1/agents" | jq -r --arg a "$address" '.agents[] | select(.signer_address==$a) | .agent_id')"
    jq -n --argjson endpoints "$rpc_endpoints" --arg chain "$CHAIN_ID" --arg rpc "$RPC_URL" --arg risk "http://$risk" --arg llm "http://$llm" \
      --arg api "http://$api" --arg id "$id" --arg key "$HOME_DIR/agent$i.key" --arg state "$HOME_DIR/agent$i-host-a" \
      '{chain_id:$chain,rpc_url:$rpc,rpc_endpoints:$endpoints,rpc_safety:{min_agreement:3,max_height_lag:2},risk_provider:"mock",risk_url:$risk,llm_provider:"mock",llm_url:$llm,
        agent_id:$id,key_file:$key,agent_api:{url:$api,token_env:"G503_AGENT_API_TOKEN",role:"validator-agent"},
        broadcast:{gas:700000,fee:"0stake"},daemon:{enabled:true,state_dir:$state}}' >"$HOME_DIR/agent$i.json"
  done
  ownership_phase policy
  start_auto_agents seed -lease-ttl-blocks 24 -expiry-height 301
  echo "[ownership 2/9] same key with independent journal cannot start while owner is alive"
  if "$BIN_DIR/sanction-agent" -config "$HOME_DIR/agent1.json" -target-type address \
      -state-dir "$HOME_DIR/agent1-duplicate" -lease-wait 0 >"$LOG_DIR/ownership-duplicate.jsonl" 2>"$LOG_DIR/ownership-duplicate.err"; then
    echo "duplicate signing owner was accepted" >&2; return 1
  fi
  grep -q 'already owned' "$LOG_DIR/ownership-duplicate.err"
  stop_auto_agents TERM
  backup_all_agents ownership
  start_auto_agents host-a -lease-ttl-blocks 24 -expiry-height 301
  local old_pids=("${auto_pids[@]}")
  kill -STOP "${old_pids[4]}"

  echo "[ownership 3/9] four resident agents detect, independently review and vote; fifth agent is paused"
  ownership_phase send
  local case_id="$(jq -r '.case.case_id' "$LOG_DIR/ownership-checkpoint.json")"
  wait_json_field "$API_URL/sanction/v1/cases/$case_id/votes" '.votes | length==4' "$LOG_DIR/ownership-four-automatic-votes.json" 120
  for i in 0 1 2 3; do kill -STOP "${old_pids[$i]}"; done
  for i in 1 2 3 4 5; do grep -c '"event":"tx_intent"' "$LOG_DIR/agent$i-host-a.jsonl" >"$LOG_DIR/ownership-old-send-count$i.txt" || true; done
  ownership_phase prepare

  echo "[ownership 4/9] add sixth validator during voting using a new node and operator"
  "$G503D_BIN" init node6 --chain-id "$CHAIN_ID" --home "$HOME_DIR/node6" >"$LOG_DIR/init-node6.log"
  cp "$HOME_DIR/genesis.combined.json" "$HOME_DIR/node6/config/genesis.json"
  perl -0pi -e 's/addr_book_strict = true/addr_book_strict = false/; s/allow_duplicate_ip = false/allow_duplicate_ip = true/' "$HOME_DIR/node6/config/config.toml"
  "$G503D_BIN" comet show-validator --home "$HOME_DIR/node6" >"$LOG_DIR/node6-validator-pubkey.json"
  start_node 6 "$LOG_DIR/node6.log"
  local faucet="$(jq -r '.faucet.address' "$HOME_DIR/node2/config/demo-keys.json")"
  local number="$(query_account_field "$faucet" account_number)" sequence="$(query_account_field "$faucet" sequence)"
  GOCACHE="$GOCACHE_DIR" go run ./dev/agent/cmd/g503-send -rpc-url "$RPC_URL" -chain-id "$CHAIN_ID" \
    -privkey-hex "$(jq -r '.faucet.private_key_hex' "$HOME_DIR/node2/config/demo-keys.json")" \
    -to "$(jq -r '.validator.address' "$HOME_DIR/node6/config/demo-keys.json")" -amount 10000000stake \
    -account-number "$number" -sequence "$sequence" -check-mempool=false >"$LOG_DIR/ownership-fund-six.json"
  wait_tx_committed "$(jq -r '.tx_hash' "$LOG_DIR/ownership-fund-six.json")" 60
  run_validator_tx 6 create-validator -pubkey-file "$LOG_DIR/node6-validator-pubkey.json" -amount 1000000stake -moniker staking-node6 >"$LOG_DIR/create-validator-node6.json"
  wait_tx_committed "$(jq -r '.tx_hash' "$LOG_DIR/create-validator-node6.json")" 60
  wait_comet_validator_count 6 "$LOG_DIR/ownership-six-validators.json" 120

  echo "[ownership 5/9] jail node5 and unbond node3; old case still requires all five"
  kill "${node_pids[5]}"; wait "${node_pids[5]}" 2>/dev/null || true
  wait_json_field "$API_URL/cosmos/staking/v1beta1/validators/$node5_valoper" '.validator.jailed == true' "$LOG_DIR/ownership-jailed.json" 120
  wait_agent_state "$node5_valoper" false 0 "$LOG_DIR/ownership-after-jail.json"
  ownership_phase unbond
  wait_agent_state "$node3_valoper" false 0 "$LOG_DIR/ownership-after-unbond.json"
  ownership_phase changed

  echo "[compound] stop primary RPC while restoring old-case voters after add/jail/unbond"
  local primary_rpc="$RPC_URL" primary_api="$API_URL"
  kill "${node_pids[1]}"; wait "${node_pids[1]}" 2>/dev/null || true
  RPC_URL="http://127.0.0.1:$((BASE_RPC_PORT+1))"
  API_URL="http://127.0.0.1:$((BASE_API_PORT+1))"
  export G503_SAFETY_RPC="$RPC_URL" G503_SAFETY_API="$API_URL"

  echo "[ownership 6/9] restore five journals to independent host-B paths and acquire new epochs"
  for i in 1 2 3 4 5; do
    local info="$LOG_DIR/backup-agent$i-ownership.json"
    "$BIN_DIR/sanction-agent" -config "$HOME_DIR/agent$i.json" -target-type address \
      -state-dir "$HOME_DIR/agent$i-host-b" -journal-restore "$HOME_DIR/agent$i-ownership.tar" \
      -backup-sha256 "$(jq -r '.sha256' "$info")" -journal-genesis-hash "$(jq -r '.manifest.genesis_hash' "$info")" >"$LOG_DIR/ownership-restore$i.json"
    jq --arg state "$HOME_DIR/agent$i-host-b" '.daemon.state_dir=$state' "$HOME_DIR/agent$i.json" >"$HOME_DIR/agent$i-b.json"
    mv "$HOME_DIR/agent$i-b.json" "$HOME_DIR/agent$i.json"
  done
  start_auto_agents host-b -lease-ttl-blocks 24 -expiry-height 301
  wait_json_field "$API_URL/sanction/v1/executions/$case_id" '.record.result_code=="ok"' "$LOG_DIR/ownership-execution.json" 120

  echo "[ownership 7/9] resume every old process and require fencing before any further send"
  for pid in "${old_pids[@]}"; do kill -CONT "$pid"; done
  for i in 1 2 3 4 5; do
    local lost=0
    for _ in $(seq 1 60); do
      if grep -q '"event":"ownership_lost"' "$LOG_DIR/agent$i-host-a.jsonl"; then lost=1; break; fi
      sleep 1
    done
    [[ "$lost" == "1" ]]
    local sends="$(grep -c '"event":"tx_intent"' "$LOG_DIR/agent$i-host-a.jsonl" || true)"
    [[ "$sends" == "$(cat "$LOG_DIR/ownership-old-send-count$i.txt")" ]]
    wait "${old_pids[$((i-1))]}" 2>/dev/null || true
  done
  if "$BIN_DIR/sanction-agent" -config "$HOME_DIR/agent1.json" -target-type address \
      -state-dir "$HOME_DIR/agent1-host-a" -lease-wait 0 >"$LOG_DIR/ownership-old-restart.jsonl" 2>"$LOG_DIR/ownership-old-restart.err"; then
    echo "old host restarted into an already owned signing account" >&2; return 1
  fi
  grep -q 'already owned' "$LOG_DIR/ownership-old-restart.err"
  stop_auto_agents TERM

  start_node 1 "$LOG_DIR/ownership-primary-rpc-return.log"
  RPC_URL="$primary_rpc"; API_URL="$primary_api"
  export G503_SAFETY_RPC="$RPC_URL" G503_SAFETY_API="$API_URL"
  wait_http "$RPC_URL/status" 90

  echo "[ownership 8/9] restart every node and check persisted epochs, snapshots and settlement"
  for i in 1 2 3 4 5 6; do kill "${node_pids[$i]}" 2>/dev/null || true; done
  for i in 1 2 3 4 5 6; do wait "${node_pids[$i]}" 2>/dev/null || true; done
  for i in 1 2 3 4 5 6; do start_node "$i" "$LOG_DIR/ownership-node$i-restart.log"; done
  for i in 1 2 3 4 5 6; do wait_http "http://127.0.0.1:$((BASE_RPC_PORT+i-1))/status" 90; done
  wait_http "$API_URL/sanction/v1/params" 90
  for i in 1 2 3 4 5 6; do
    wait_json_field "http://127.0.0.1:$((BASE_API_PORT+i-1))/sanction/v1/executions/$case_id" \
      '.record.result_code=="ok"' "$LOG_DIR/ownership-node$i-persisted-execution.json" 90
  done
  ownership_phase final
  jq '. + {rpc_outage_during_churn_and_takeover:true,rpc_pool_endpoints:5,rpc_min_agreement:3}' "$LOG_DIR/ownership-metrics.json" >"$LOG_DIR/ownership-metrics.tmp"
  mv "$LOG_DIR/ownership-metrics.tmp" "$LOG_DIR/ownership-metrics.json"
  echo "[ownership 9/9] verified duplicate startup rejection, old-host fencing and fixed voting"
  cat "$LOG_DIR/ownership-metrics.json"
}
