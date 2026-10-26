#!/usr/bin/env bash
set -euo pipefail
umask 077
root="$1"
run="$2"
owner="$3"
action="$4"
shift 4
[[ "$root" == /* && "$root" != / && "$run" =~ ^[a-zA-Z0-9_-]+$ ]]
[[ "$(cat "$root/.g503-lab-owned")" == "$owner" ]]
chain="G503_$(printf '%s' "$run" | sha256sum | cut -c1-12)"

launch() {
  local name="$1"
  shift
  local log="$root/logs/$name-$(date +%s%N).log"
  nohup "$@" </dev/null >"$log" 2>&1 &
  printf '%s' "$!" >"$root/pids/$name"
  printf '%s' "$log" >"$root/$name.logpath"
}

stop_owned() {
  local name="$1" pid cmd
  [[ -f "$root/pids/$name" ]] || return 0
  pid="$(cat "$root/pids/$name")"
  [[ "$pid" =~ ^[0-9]+$ && "$pid" -gt 1 ]]
  [[ -r "/proc/$pid/cmdline" ]] || return 0
  cmd="$(tr '\0' ' ' <"/proc/$pid/cmdline")"
  [[ "$cmd" == *"$root/bin/"* ]] || { echo "refusing unrelated PID" >&2; return 1; }
  kill -TERM "$pid" 2>/dev/null || true
  for _ in $(seq 1 30); do
    [[ -r "/proc/$pid/cmdline" ]] || return 0
    cmd="$(tr '\0' ' ' <"/proc/$pid/cmdline")"
    [[ "$cmd" == *"$root/bin/"* ]] || return 0
    sleep 1
  done
  kill -KILL "$pid"
}

heal() {
  [[ -f "$root/firewall.owner" ]] || return 0
  [[ "$(cat "$root/firewall.owner")" == "$owner" ]]
  sudo -n iptables -F "$chain"
  for direction in INPUT OUTPUT; do
    if sudo -n iptables -C "$direction" -j "$chain" 2>/dev/null; then sudo -n iptables -D "$direction" -j "$chain"; fi
  done
  sudo -n iptables -X "$chain"
  rm "$root/firewall.owner"
}

start_node() {
  launch node "$root/bin/g503d" start --home "$root/node" --chain-id "$(cat "$root/chain-id")" \
    --minimum-gas-prices 0stake --rpc.laddr tcp://0.0.0.0:26657 --p2p.laddr tcp://0.0.0.0:26656 \
    --p2p.persistent_peers "$(cat "$root/peers")" --api.enable --api.address tcp://0.0.0.0:1317 --grpc.address 127.0.0.1:9090
}

start_agent() {
  export G503_AGENT_API_TOKEN
  G503_AGENT_API_TOKEN="$(jq -r '.token' "$root/secret.json")"
  launch agent "$root/bin/sanction-agent" -config "$root/agent.json" -target-type address -expiry-height 301 -poll-interval 500ms -lease-wait 90s
}

case "$action" in
  init)
    mkdir -p "$root/bin" "$root/logs" "$root/pids"
    cd "$root/source"
    export GOFLAGS=-p=2
    for pair in 'g503d cmd/g503d' 'sanction-agent dev/agent/cmd/sanction-agent' 'g503-send dev/agent/cmd/g503-send' 'risk dev/mock/risk-service' 'llm dev/mock/llm-service' 'agent-api dev/mock/agent-api'; do
      read -r name path <<<"$pair"
      go build -o "$root/bin/$name" "./$path"
    done
    "$root/bin/g503d" init "$2" --home "$root/node" --chain-id "$1" >"$root/logs/init.log"
    printf '%s' "$1" >"$root/chain-id"
    perl -0pi -e 's/addr_book_strict = true/addr_book_strict = false/' "$root/node/config/config.toml"
    jq -r '.agent.private_key_hex' "$root/node/config/demo-keys.json" >"$root/agent.key"
    ;;
  genesis) cat "$root/node/config/genesis.json" ;;
  node-id) "$root/bin/g503d" comet show-node-id --home "$root/node" ;;
  start-node)
    cp "$root/genesis.json" "$root/node/config/genesis.json"
    printf '%s' "$2" >"$root/peers"
    start_node
    ;;
  restart-node) start_node ;;
  stop-node) stop_owned node ;;
  start-services)
    export G503_AGENT_API_TOKEN
    G503_AGENT_API_TOKEN="$(jq -r '.token' "$root/secret.json")"
    recipient="$(jq -r '.demo.address' "$root/node/config/demo-keys.json")"
    launch risk "$root/bin/risk" -listen 0.0.0.0:8081 -high-risk-addresses "$recipient" -high-risk-score 95
    launch llm "$root/bin/llm" -listen 0.0.0.0:8082
    launch agent-api "$root/bin/agent-api" -listen 0.0.0.0:8083
    ;;
  start-agent) start_agent ;;
  restart-agent) stop_owned agent; start_agent ;;
  agent-ready)
    pid="$(cat "$root/pids/agent")"
    kill -0 "$pid"
    grep -q '"event":"ready"' "$(cat "$root/agent.logpath")"
    ;;
  state)
    status="$(curl -fsS --max-time 5 http://127.0.0.1:26657/status)"
    sends="$(find "$root/logs" -name 'agent-*.log' -exec grep -h '"event":"rpc_broadcast"' {} + | wc -l || true)"
    jq -n --argjson status "$status" --argjson sends "$sends" '{status:$status,sends:$sends}'
    ;;
  block-hash)
    [[ "$1" =~ ^[1-9][0-9]*$ ]]
    curl -fsS --max-time 5 "http://127.0.0.1:26657/block?height=$1" | jq -er '.result.block_id.hash'
    ;;
  transfer)
    faucet="$(jq -r '.faucet.address' "$root/node/config/demo-keys.json")"
    recipient="$(jq -r '.demo.address' "$root/node/config/demo-keys.json")"
    account="$(curl -fsS --max-time 5 "http://127.0.0.1:1317/cosmos/auth/v1beta1/accounts/$faucet" | jq '.account')"
    number="$(jq -r '.account_number // .base_account.account_number // .value.account_number' <<<"$account")"
    sequence="$(jq -r '.sequence // .base_account.sequence // .value.sequence' <<<"$account")"
    "$root/bin/g503-send" -rpc-url http://127.0.0.1:26657 -chain-id "$(cat "$root/chain-id")" \
      -privkey-hex "$(jq -r '.faucet.private_key_hex' "$root/node/config/demo-keys.json")" \
      -to "$recipient" -amount 1stake -account-number "$number" -sequence "$sequence" -fee 0stake -check-mempool=false
    ;;
  settlement|unsettled)
    [[ "$1" =~ ^[a-fA-F0-9]{64}$ ]]
    data="$(curl -fsS --max-time 5 "http://127.0.0.1:1317/sanction/v1/tx/$1/settlements")"
    if [[ "$action" == unsettled ]]; then jq -e '.settlements | length == 0' <<<"$data" >/dev/null
    else
      jq -e '.settlements | length == 1' <<<"$data" >/dev/null
      case_id="$(jq -r '.settlements[0].case_id' <<<"$data")"
      curl -fsS --max-time 5 "http://127.0.0.1:1317/sanction/v1/cases/$case_id/votes" | jq -e '.votes | length == 5' >/dev/null
      for address in $(jq -r '.settlements[0].transfer | .sender,.recipient' <<<"$data"); do
        curl -fsS --max-time 5 "http://127.0.0.1:1317/cosmos/bank/v1beta1/balances/$address/by_denom?denom=stake" | jq -e '.balance.amount == "1000000000"' >/dev/null
      done
    fi
    ;;
  partition)
    heal
    sudo -n iptables -N "$chain"
    printf '%s' "$owner" >"$root/firewall.owner"
    for direction in INPUT OUTPUT; do sudo -n iptables -I "$direction" 1 -j "$chain"; done
    for peer in "$@"; do
      [[ "$peer" =~ ^[0-9.]+$ ]]
      sudo -n iptables -A "$chain" -p tcp -s "$peer" -m multiport --ports 26656,26657,1317,8081,8082,8083 -j DROP
      sudo -n iptables -A "$chain" -p tcp -d "$peer" -m multiport --ports 26656,26657,1317,8081,8082,8083 -j DROP
    done
    ;;
  heal) heal ;;
  cleanup)
    heal
    for process in agent node risk llm agent-api; do stop_owned "$process"; done
    ;;
  collect) COPYFILE_DISABLE=1 tar --no-xattrs -C "$root" -czf - logs ;;
  *) echo "unsupported multihost worker action" >&2; exit 2 ;;
esac
