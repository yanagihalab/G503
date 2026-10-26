#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOME_DIR="${G503_HOME:-/private/tmp/g503-local}"
RPC_URL="${G503_RPC_URL:-http://127.0.0.1:26657}"
API_URL="${G503_API_URL:-}"
KEYS_FILE="${KEYS_FILE:-$HOME_DIR/config/demo-keys.json}"
FROM_KEY="${FROM_KEY:-faucet}"
TO_KEY="${TO_KEY:-demo}"
AMOUNT="${AMOUNT:-1stake}"
FEE="${FEE:-0stake}"
SEQUENCE="${SEQUENCE:-}"

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 1
fi

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

chain_id="$(jq -r '.chain_id' "$KEYS_FILE")"
from_addr="$(jq -r --arg key "$FROM_KEY" '.[$key].address' "$KEYS_FILE")"
from_priv="$(jq -r --arg key "$FROM_KEY" '.[$key].private_key_hex' "$KEYS_FILE")"
to_addr="$(jq -r --arg key "$TO_KEY" '.[$key].address' "$KEYS_FILE")"
if [[ -n "$API_URL" ]]; then
  from_account="$(query_account_field "$API_URL" "$from_addr" account_number)"
else
  from_account="$(jq -r --arg key "$FROM_KEY" '.[$key].account_number' "$KEYS_FILE")"
fi
if [[ -z "$SEQUENCE" ]]; then
  if [[ -n "$API_URL" ]]; then
    SEQUENCE="$(query_account_field "$API_URL" "$from_addr" sequence)"
  else
    SEQUENCE="$(jq -r --arg key "$FROM_KEY" '.[$key].sequence' "$KEYS_FILE")"
  fi
fi

cd "$ROOT_DIR"
GOCACHE="${GOCACHE:-$ROOT_DIR/../../.gocache}" go run ./dev/agent/cmd/g503-send \
  -rpc-url "$RPC_URL" \
  -chain-id "$chain_id" \
  -from "$from_addr" \
  -to "$to_addr" \
  -amount "$AMOUNT" \
  -privkey-hex "$from_priv" \
  -account-number "$from_account" \
  -sequence "$SEQUENCE" \
  -fee "$FEE" \
  -check-mempool
