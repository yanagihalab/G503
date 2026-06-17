#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TX_JSON="${1:-/tmp/sanction-agent-eval-tx.json}"
RISK_URL="${RISK_URL:-http://127.0.0.1:8081}"
LLM_URL="${LLM_URL:-http://127.0.0.1:8082}"

if [[ ! -f "$TX_JSON" ]]; then
  cat > "$TX_JSON" <<'JSON'
{"tx_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sender":"cosmos1sender","recipient":"cosmos1scamrecipient","amount":"1000000","denom":"utoken"}
JSON
fi

start_ns="$(date +%s%N)"
GOCACHE="${GOCACHE:-$ROOT/.gocache}" go run ./dev/agent/cmd/sanction-agent \
  -tx "$TX_JSON" \
  -risk-url "$RISK_URL" \
  -llm-url "$LLM_URL" >/tmp/sanction-agent-eval-output.json
end_ns="$(date +%s%N)"

latency_ms="$(( (end_ns - start_ns) / 1000000 ))"
printf '{"latency_ms":%s,"output":' "$latency_ms"
cat /tmp/sanction-agent-eval-output.json
printf '}\n'
