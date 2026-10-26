#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TX_JSON="${1:-/tmp/g503-real-integration-tx.json}"
RISK_URL="${RISK_URL:-https://api.chainalysis.com}"
LLM_URL="${LLM_URL:-http://127.0.0.1:11434}"
LLM_MODEL="${LLM_MODEL:-llama3.1}"

if [[ -z "${CHAINALYSIS_API_KEY:-}" ]]; then
  echo "CHAINALYSIS_API_KEY is required for real Chainalysis screening" >&2
  exit 2
fi

if [[ ! -f "$TX_JSON" ]]; then
  cat > "$TX_JSON" <<'JSON'
{"tx_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sender":"g5031sender","recipient":"g5031recipient","amount":"100","denom":"stake"}
JSON
fi

GOCACHE="${GOCACHE:-$ROOT/.gocache}" go run ./dev/agent/cmd/sanction-agent \
  -tx "$TX_JSON" \
  -risk-provider chainalysis \
  -risk-url "$RISK_URL" \
  -risk-api-key "$CHAINALYSIS_API_KEY" \
  -llm-provider ollama \
  -llm-url "$LLM_URL" \
  -llm-model "$LLM_MODEL"
