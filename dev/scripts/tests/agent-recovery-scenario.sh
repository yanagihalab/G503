#!/usr/bin/env bash
# Sourced into the five-node fixture. Backups never include private keys.

backup_all_agents() {
  local label="$1"
  for i in 1 2 3 4 5; do
    "$BIN_DIR/sanction-agent" -config "$HOME_DIR/agent$i.json" \
      -journal-backup "$HOME_DIR/agent$i-$label.tar" >"$LOG_DIR/backup-agent$i-$label.json"
  done
}

restore_all_agents() {
  local label="$1" generation="$2"
  for i in 1 2 3 4 5; do
    local state archive_info
    state="$(jq -r '.daemon.state_dir' "$HOME_DIR/agent$i.json")"
    archive_info="$LOG_DIR/backup-agent$i-$label.json"
    if [[ -e "$state" ]]; then mv "$state" "$state-held-$generation"; fi
    env -u G503_AGENT_API_TOKEN "$BIN_DIR/sanction-agent" -config "$HOME_DIR/agent$i.json" -target-type address \
      -rpc-url http://127.0.0.1:1 \
      -journal-restore "$HOME_DIR/agent$i-$label.tar" \
      -backup-sha256 "$(jq -r '.sha256' "$archive_info")" \
      -journal-genesis-hash "$(jq -r '.manifest.genesis_hash' "$archive_info")" \
      >"$LOG_DIR/restore-agent$i-$generation.json"
  done
}

recovery_snapshots() {
  local label="$1"
  for i in 1 2 3 4 5; do
    "$BIN_DIR/sanction-agent" -config "$HOME_DIR/agent$i.json" -daemon-status >"$LOG_DIR/recovery-agent$i-$label.json"
  done
}

wait_recovery_height() {
  local target="$1"
  for _ in $(seq 1 120); do
    if [[ "$(curl -fsS "$RPC_URL/status" | jq -r '.result.sync_info.latest_block_height')" -gt "$target" ]]; then return 0; fi
    sleep 1
  done
  echo "chain did not advance past recovery checkpoint $target" >&2
  return 1
}

save_agent_sequences() {
  local output="$1"
  : >"$output"
  for i in 1 2 3 4 5; do
    query_account_field "$API_URL" "$(jq -r '.agent.address' "$HOME_DIR/node$i/config/demo-keys.json")" sequence >>"$output"
  done
}

wait_agent_completion() {
  local generation="$1" id="$2"
  for i in 1 2 3 4 5; do
    local done=0
    for _ in $(seq 1 120); do
      if jq -s -e --arg id "$id" 'any(.[]; .case_id==$id and .event=="complete")' "$LOG_DIR/agent$i-$generation.jsonl" >/dev/null 2>&1; then done=1; break; fi
      sleep 1
    done
    [[ "$done" == "1" ]]
  done
}

run_agent_recovery_scenario() {
  export G503_SAFETY_HOME="$HOME_DIR" G503_SAFETY_RPC="$RPC_URL" G503_SAFETY_API="$API_URL" G503_CHAIN_ID="$CHAIN_ID"
  jq -n --arg sender "$(auto_balance "$auto_faucet")" --arg recipient "$(auto_balance "$demo_addr")" \
    '{sender:$sender,recipient:$recipient}' >"$LOG_DIR/recovery-balances.json"
  echo "[recovery 1/7] fail signed sends with insufficient gas on all five agents"
  for i in 1 2 3 4 5; do
    jq --arg rpc "http://127.0.0.1:$((BASE_RPC_PORT+i-1))" --arg risk "$RISK_REVERT_URL" \
      --arg api "http://${G503_AUTO_API_LISTEN:-127.0.0.1:59204}" --arg state "$HOME_DIR/agent$i-recovery-state" \
      '.rpc_url=$rpc | .risk_url=$risk | .agent_api={url:$api,token_env:"G503_AGENT_API_TOKEN",role:"validator-agent"} | .daemon.state_dir=$state | .broadcast.gas=1' \
      "$HOME_DIR/agent$i.json" >"$HOME_DIR/agent$i-recovery.json"
    mv "$HOME_DIR/agent$i-recovery.json" "$HOME_DIR/agent$i.json"
  done
  start_auto_agents recovery-fail -expiry-height 13
  auto_send_transfer 13stake "$LOG_DIR/recovery-transfer1.json"
  local hash1 case1
  hash1="$(jq -r '.tx_hash' "$LOG_DIR/recovery-transfer1.json")"; case1="auto-$hash1"
  for i in 1 2 3 4 5; do
    local failed=0
    for _ in $(seq 1 90); do
      if jq -s -e --arg id "open/$case1" 'any(.[]; .id==$id and .status=="failed")' "$LOG_DIR/agent$i-recovery-fail.jsonl" >/dev/null 2>&1; then failed=1; break; fi
      sleep 1
    done
    [[ "$failed" == "1" ]]
  done
  stop_auto_agents TERM
  recovery_snapshots failed
  backup_all_agents failed
  local expiry=0 e
  for i in 1 2 3 4 5; do
    e="$(jq --arg id "$case1" '[.work[] | select(.case_id==$id) | .review.risk_report.expiry_height|tonumber] | max' "$LOG_DIR/recovery-agent$i-failed.json")"
    if [[ "$e" -gt "$expiry" ]]; then expiry="$e"; fi
  done
  wait_recovery_height "$((expiry+1))"

  echo "[recovery 2/7] repair gas, re-evaluate expired evidence and explicitly retry failed intents"
  for i in 1 2 3 4 5; do
    jq '.broadcast.gas=700000' "$HOME_DIR/agent$i.json" >"$HOME_DIR/agent$i-fixed.json"
    mv "$HOME_DIR/agent$i-fixed.json" "$HOME_DIR/agent$i.json"
  done
  start_auto_agents recovery-retry -retry-intent "open/$case1" -retry-reason "repair gas and independently re-screen expired evidence"
  wait_json_field "$API_URL/sanction/v1/executions/$case1" '.record.result_code=="ok"' "$LOG_DIR/recovery-execution1.json" 180
  wait_agent_completion recovery-retry "$case1"
  stop_auto_agents TERM
  recovery_snapshots retried
  save_agent_sequences "$LOG_DIR/recovery-sequences-before.txt"
  curl -fsS "$RPC_URL/status" | jq -r '.result.sync_info.latest_block_height' >"$LOG_DIR/recovery-restore-start-height.txt"

  echo "[recovery 3/7] restore old failed snapshots after success; reconcile without sending again"
  restore_all_agents failed stale-failed
  start_auto_agents recovery-restored
  wait_agent_completion recovery-restored "$case1"
  sleep 3
  stop_auto_agents TERM
  recovery_snapshots restored
  save_agent_sequences "$LOG_DIR/recovery-sequences-after.txt"
  curl -fsS "$RPC_URL/status" | jq -r '.result.sync_info.latest_block_height' >"$LOG_DIR/recovery-restore-end-height.txt"

  echo "[recovery 4/7] interrupt voting with a lost open acknowledgement"
  local proxy_listen="${G503_RECOVERY_PROXY_LISTEN:-127.0.0.1:60207}"
  "$BIN_DIR/rpc-fault-proxy" -listen "$proxy_listen" -upstream "$RPC_URL" -message-type open \
    -marker "$LOG_DIR/recovery-lost-open.json" >"$LOG_DIR/recovery-proxy.log" 2>&1 &
  pids+=("$!")
  wait_http "http://$proxy_listen/status" 30
  jq --arg rpc "http://$proxy_listen" '.rpc_url=$rpc' "$HOME_DIR/agent1.json" >"$HOME_DIR/agent1-proxy.json"
  mv "$HOME_DIR/agent1-proxy.json" "$HOME_DIR/agent1.json"
  start_auto_agents recovery-pending -expiry-height 13
  auto_send_transfer 7stake "$LOG_DIR/recovery-transfer2.json"
  local hash2 case2 injected=0
  hash2="$(jq -r '.tx_hash' "$LOG_DIR/recovery-transfer2.json")"; case2="auto-$hash2"
  for _ in $(seq 1 120); do if [[ -s "$LOG_DIR/recovery-lost-open.json" ]]; then injected=1; break; fi; sleep 0.1; done
  [[ "$injected" == "1" ]]
  stop_auto_agents KILL
  wait_json_field "$API_URL/sanction/v1/cases/$case2" '.sanction_case.case_id' "$LOG_DIR/recovery-expired-case.json" 60
  local report_id deadline report_expiry
  report_id="$(jq -r '.sanction_case.report_ids[0]' "$LOG_DIR/recovery-expired-case.json")"
  deadline="$(jq -r '.sanction_case.voting_deadline_height' "$LOG_DIR/recovery-expired-case.json")"
  curl -fsS "$API_URL/sanction/v1/risk_reports/$report_id" >"$LOG_DIR/recovery-old-report.json"
  report_expiry="$(jq -r '.report.expiry_height' "$LOG_DIR/recovery-old-report.json")"
  if [[ "$report_expiry" -gt "$deadline" ]]; then deadline="$report_expiry"; fi
  wait_recovery_height "$((deadline+1))"

  echo "[recovery 5/7] renew expired case and obtain five fresh signatures"
  jq --arg rpc "$RPC_URL" '.rpc_url=$rpc' "$HOME_DIR/agent1.json" >"$HOME_DIR/agent1-direct.json"
  mv "$HOME_DIR/agent1-direct.json" "$HOME_DIR/agent1.json"
  start_auto_agents recovery-round
  wait_json_field "$API_URL/sanction/v1/executions/$case2-r1" '.record.result_code=="ok"' "$LOG_DIR/recovery-execution2.json" 180
  wait_agent_completion recovery-round "$case2-r1"
  stop_auto_agents TERM
  recovery_snapshots renewed

  echo "[recovery 6/7] verify recovery histories, signatures, event counts and balance restoration"
  GOCACHE="$GOCACHE_DIR" go test -p 2 -count=1 -v ./dev/agent/e2e -run '^TestAgentRecovery$' | tee "$LOG_DIR/recovery-test.log"
  echo "[recovery 7/7] save recovery metrics"
  cat "$LOG_DIR/recovery-metrics.json"
}
