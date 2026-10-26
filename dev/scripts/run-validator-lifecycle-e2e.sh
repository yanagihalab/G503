#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOME_DIR="${G503_LIFECYCLE_HOME:-/private/tmp/g503-validator-lifecycle-e2e}"
CHAIN_ID="${G503_CHAIN_ID:-g503-validator-lifecycle-1}"
BASE_RPC_PORT="${G503_BASE_RPC_PORT:-55657}"
BASE_P2P_PORT="${G503_BASE_P2P_PORT:-56656}"
BASE_API_PORT="${G503_BASE_API_PORT:-23317}"
BASE_GRPC_PORT="${G503_BASE_GRPC_PORT:-50090}"
GOCACHE_DIR="${GOCACHE:-$ROOT_DIR/../../.gocache}"
LOG_DIR="$HOME_DIR/logs"
G503D_BIN="$HOME_DIR/g503d"
RPC_URL="http://127.0.0.1:$BASE_RPC_PORT"
API_URL="http://127.0.0.1:$BASE_API_PORT"

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 1
fi
if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required" >&2
  exit 1
fi

cd "$ROOT_DIR"

pids=()
node_pids=()
cleanup() {
  for pid in "${pids[@]:-}"; do
    if kill -0 "$pid" >/dev/null 2>&1; then
      kill -CONT "$pid" >/dev/null 2>&1 || true
      kill "$pid" >/dev/null 2>&1 || true
    fi
  done
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

query_account_field() {
  local address="$1"
  local field="$2"
  curl -fsS "$API_URL/cosmos/auth/v1beta1/accounts/$address" | jq -r --arg field "$field" '
    .account as $account |
    if $field == "account_number" then
      ($account.account_number // $account.base_account.account_number // $account.value.account_number // $account.value.base_account.account_number)
    else
      ($account.sequence // $account.base_account.sequence // $account.value.sequence // $account.value.base_account.sequence)
    end
  '
}

wait_tx_committed() {
  local hash="$1"
  local attempts="$2"
  for _ in $(seq 1 "$attempts"); do
    if curl -fsS "$RPC_URL/tx?hash=0x$hash" | jq -e '.result.tx_result.code == 0' >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for tx $hash" >&2
  return 1
}

wait_comet_validator_count() {
  local expected="$1"
  local output="$2"
  local attempts="$3"
  for _ in $(seq 1 "$attempts"); do
    if curl -fsS "$RPC_URL/validators" >"$output" 2>/dev/null && jq -e --argjson expected "$expected" '.result.validators | length == $expected' "$output" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for CometBFT validator count $expected" >&2
  return 1
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
  "$G503D_BIN" start \
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
  node_pids[$node_index]="$!"
}

run_validator_tx() {
  local node_index="$1"
  local action="$2"
  shift 2
  local keys_file="$HOME_DIR/node$node_index/config/demo-keys.json"
  local val_addr val_priv account sequence
  val_addr="$(jq -r '.validator.address' "$keys_file")"
  val_priv="$(jq -r '.validator.private_key_hex' "$keys_file")"
  account="$(query_account_field "$val_addr" account_number)"
  sequence="$(query_account_field "$val_addr" sequence)"
  GOCACHE="$GOCACHE_DIR" go run ./dev/agent/cmd/g503-validator "$action" \
    -rpc-url "$RPC_URL" \
    -chain-id "$CHAIN_ID" \
    -privkey-hex "$val_priv" \
    -account-number "$account" \
    -sequence "$sequence" \
    -gas 800000 \
    -fee 0stake \
    "$@"
}

wait_agent_state() {
  local valoper="$1"
  local active="$2"
  local power="$3"
  local output="$4"
  for _ in $(seq 1 90); do
    if curl -fsS "$API_URL/sanction/v1/agents" >"$output" 2>/dev/null && jq -e --arg valoper "$valoper" --argjson active "$active" --arg power "$power" '
        [.agents[] | select((.validator_operator_address // .validatorOperatorAddress) == $valoper)
          | select((.active == $active) and (((.voting_power // .votingPower) | tostring) == $power))]
        | length == 1
      ' "$output" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for agent $valoper active=$active power=$power" >&2
  return 1
}

echo "[1/9] initialize lifecycle homes: $HOME_DIR"
rm -rf "$HOME_DIR"
mkdir -p "$LOG_DIR"
GOCACHE="$GOCACHE_DIR" go build -o "$G503D_BIN" ./cmd/g503d
genesis_files=()
node_ids=()
for i in 1 2 3 4 5; do
  home="$HOME_DIR/node$i"
  "$G503D_BIN" init "node$i" --chain-id "$CHAIN_ID" --home "$home" >"$LOG_DIR/init-node$i.log"
  perl -0pi -e 's/addr_book_strict = true/addr_book_strict = false/; s/allow_duplicate_ip = false/allow_duplicate_ip = true/' "$home/config/config.toml"
  "$G503D_BIN" comet show-validator --home "$home" >"$LOG_DIR/node$i-validator-pubkey.json"
  genesis_files+=("$home/config/genesis.json")
  node_ids+=("$("$G503D_BIN" comet show-node-id --home "$home")")
done

echo "[2/9] compose genesis with three initial consensus validators and five funded validator accounts"
jq -s '
  .[0] as $base |
  ([.[0:3][].validators[0]]) as $validators |
  ([.[].app_state.auth.accounts[] | select(."@type" == "/cosmos.auth.v1beta1.ModuleAccount")] | unique_by(.base_account.address)) as $modules |
  ([.[].app_state.auth.accounts[] | select(."@type" == "/cosmos.auth.v1beta1.BaseAccount")] | unique_by(.address) | to_entries | map(.value.account_number = ((.key + 10) | tostring) | .value.sequence = "0" | .value)) as $users |
  ([.[].app_state.bank.balances[]] | unique_by(.address)) as $balances |
  $base
  | .validators = $validators
  | .app_state.auth.accounts = ($modules + $users)
  | .app_state.bank.balances = $balances
  | .app_state.bank.supply = [{"denom":"stake","amount": (($balances | length * 1000000000) | tostring)}]
  | .app_state.sanction.agents = []
  | .app_state.slashing.params.signed_blocks_window = "5"
  | .app_state.slashing.params.min_signed_per_window = "1.000000000000000000"
  | .app_state.slashing.params.downtime_jail_duration = "5s"
' "${genesis_files[@]}" >"$HOME_DIR/genesis.combined.json"
for i in 1 2 3 4 5; do
  cp "$HOME_DIR/genesis.combined.json" "$HOME_DIR/node$i/config/genesis.json"
done

peers=()
for i in 1 2 3 4 5; do
  p2p_port="$((BASE_P2P_PORT + i - 1))"
  peers+=("${node_ids[$((i - 1))]}@127.0.0.1:$p2p_port")
done

echo "[3/9] start four nodes with three genesis validators"
for i in 1 2 3 4 5; do
  start_node "$i" "$LOG_DIR/node$i.log"
done
wait_http "$RPC_URL/status" 90
wait_http "$API_URL/sanction/v1/params" 90

echo "[4/9] create five staking validators"
for i in 1 2 3 4 5; do
  run_validator_tx "$i" create-validator \
    -pubkey-file "$LOG_DIR/node$i-validator-pubkey.json" \
    -amount 1000000stake \
    -moniker "staking-node$i" \
    >"$LOG_DIR/create-validator-node$i.json"
  wait_tx_committed "$(jq -r '.tx_hash' "$LOG_DIR/create-validator-node$i.json")" 60
done
wait_json_field "$API_URL/cosmos/staking/v1beta1/validators?status=BOND_STATUS_BONDED" \
  '.validators | length == 5' "$LOG_DIR/staking-validators-bonded.json" 120
wait_json_field "$API_URL/cosmos/slashing/v1beta1/signing_infos" \
  '.info | length == 5' "$LOG_DIR/slashing-signing-infos.json" 120
wait_comet_validator_count 5 "$LOG_DIR/comet-validators-five.json" 120
wait_json_field "$API_URL/sanction/v1/agents" \
  '[.agents[] | select(.active == true and (.public_key | length > 0))] | length == 5' "$LOG_DIR/agents-after-create.json" 120
node3_valoper="$(jq -r '.validator' "$LOG_DIR/create-validator-node3.json")"
node5_valoper="$(jq -r '.validator' "$LOG_DIR/create-validator-node5.json")"

if [[ "${G503_OWNERSHIP_E2E:-0}" == "1" ]]; then
  source "$ROOT_DIR/dev/scripts/tests/ownership-snapshot-scenario.sh"
  run_ownership_snapshot_scenario
  exit 0
fi

echo "[5/9] stop node5 and wait for slashing jail"
kill "${node_pids[5]}" >/dev/null 2>&1 || true
wait "${node_pids[5]}" 2>/dev/null || true
wait_json_field "$API_URL/cosmos/staking/v1beta1/validators/$node5_valoper" \
  '.validator.jailed == true' "$LOG_DIR/node5-jailed.json" 120
wait_agent_state "$node5_valoper" false 0 "$LOG_DIR/agents-after-jail.json"

echo "[6/9] unbond node3 self delegation and verify registry deactivation"
run_validator_tx 3 unbond -amount 1000000stake >"$LOG_DIR/unbond-node3.json"
wait_tx_committed "$(jq -r '.tx_hash' "$LOG_DIR/unbond-node3.json")" 60
wait_json_field "$API_URL/cosmos/staking/v1beta1/validators/$node3_valoper" \
  '.validator.status != "BOND_STATUS_BONDED"' "$LOG_DIR/node3-unbonding.json" 90
wait_agent_state "$node3_valoper" false 0 "$LOG_DIR/agents-after-unbond.json"

echo "[7/9] confirm final validator and agent state"
curl -fsS "$API_URL/cosmos/staking/v1beta1/validators" >"$LOG_DIR/staking-validators-final.json"
curl -fsS "$API_URL/sanction/v1/agents" >"$LOG_DIR/agents-final.json"
active_agents="$(jq '[.agents[] | select(.active == true)] | length' "$LOG_DIR/agents-final.json")"
if [[ "$active_agents" != "3" ]]; then
  echo "expected 3 active agents after node5 jail and node3 unbond, got $active_agents" >&2
  exit 1
fi

echo "[8/9] write metrics"
jq -n \
  --arg jailed_validator "$node5_valoper" \
  --arg unbonded_validator "$node3_valoper" \
  --argjson active_agents "$active_agents" \
  --arg logs "$LOG_DIR" \
  '{
    validators_before_jail: 5,
    jailed_validator: $jailed_validator,
    unbonded_validator: $unbonded_validator,
    active_agents_after_jail_and_unbond: $active_agents,
    jail_observed: true,
    unbond_observed: true,
    logs: $logs
  }' >"$LOG_DIR/validator-lifecycle-metrics.json"
cat "$LOG_DIR/validator-lifecycle-metrics.json"
echo "validator lifecycle E2E completed"
