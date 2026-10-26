#!/usr/bin/env bash
# Sourced after the autonomous crash-recovery scenario; nodes are still running.

run_sanction_safety_scenario() {
  export G503_SAFETY_HOME="$HOME_DIR"
  export G503_SAFETY_RPC="$RPC_URL"
  export G503_SAFETY_API="$API_URL"
  export G503_CHAIN_ID="$CHAIN_ID"

  echo "[safety 1/5] verify original receipts and reject fully approved duplicate recovery"
  GOCACHE="$GOCACHE_DIR" go test -p 2 -count=1 -v ./dev/agent/e2e -run '^TestSettlementSafety$' \
    | tee "$LOG_DIR/safety-settlement-test.log"

  echo "[safety 2/5] start five independent agents with address-freeze policy"
  for i in 1 2 3 4 5; do
    jq --arg risk "$RISK_BLOCK_URL" --arg rpc "http://127.0.0.1:$((BASE_RPC_PORT+i-1))" \
      --arg state "$HOME_DIR/agent$i-safety-state" \
      '.risk_url=$risk | .rpc_url=$rpc | .agent_api={} | .daemon.state_dir=$state' \
      "$HOME_DIR/agent$i.json" >"$HOME_DIR/agent$i-safety.json"
    mv "$HOME_DIR/agent$i-safety.json" "$HOME_DIR/agent$i.json"
  done
  start_auto_agents safety

  echo "[safety 3/5] automatic freeze, signed review, authorized revoke, ordinary send"
  local review_risk_listen="${G503_REVIEW_RISK_LISTEN:-127.0.0.1:60206}"
  "$BIN_DIR/risk-service" -listen "$review_risk_listen" \
    -high-risk-addresses "$demo_addr" -high-risk-score 12 -high-risk-categories low_risk \
    >"$LOG_DIR/risk-corrected.log" 2>&1 &
  pids+=("$!")
  export G503_REVIEW_RISK_URL="http://$review_risk_listen"
  export G503_REVIEW_AGENT_API="http://${G503_AUTO_API_LISTEN:-127.0.0.1:59204}"
  wait_http "$G503_REVIEW_RISK_URL/v1/address/$demo_addr/risk" 30
  GOCACHE="$GOCACHE_DIR" go test -p 2 -count=1 -v ./dev/agent/e2e -run '^TestFreezeReviewRevoke$' \
    | tee "$LOG_DIR/safety-review-test.log"
  stop_auto_agents TERM

  echo "[safety 4/5] restart all five validators with existing chain state"
  for pid in "${node_pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${node_pids[@]}"; do wait "$pid" 2>/dev/null || true; done
  for i in 1 2 3 4 5; do
    if curl --max-time 2 -fsS "http://127.0.0.1:$((BASE_RPC_PORT+i-1))/status" >/dev/null 2>&1; then
      echo "node$i was not stopped" >&2
      return 1
    fi
  done
  node_pids=()
  for i in 1 2 3 4 5; do start_node "$i" "$LOG_DIR/node$i-safety-restart.log"; done
  for i in 1 2 3 4 5; do
    wait_http "http://127.0.0.1:$((BASE_RPC_PORT+i-1))/status" 90
    wait_http "http://127.0.0.1:$((BASE_API_PORT+i-1))/sanction/v1/authority" 90
  done

  echo "[safety 5/5] confirm persistent settlement and audit on every node; reject replay"
  GOCACHE="$GOCACHE_DIR" go test -p 2 -count=1 -v ./dev/agent/e2e -run '^TestSafetyPersistence$' \
    | tee "$LOG_DIR/safety-persistence-test.log"
  cat "$LOG_DIR/safety-metrics.json"
}
