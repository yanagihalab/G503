#!/usr/bin/env bash
set -euo pipefail

COMPOSE_FILE="${COMPOSE_FILE:-docker/docker-compose.yml}"
DENOM="${DENOM:-stake}"
CHAIN_A_ID="${CHAIN_A_ID:-crossref-a}"
CHAIN_B_ID="${CHAIN_B_ID:-crossref-b}"
CHAIN_C_ID="${CHAIN_C_ID:-crossref-c}"
CHAIN_D_ID="${CHAIN_D_ID:-crossref-d}"
CHAIN_E_ID="${CHAIN_E_ID:-crossref-e}"
BLOCK_TIME_UNIX="${BLOCK_TIME_UNIX:-0}"

dc() {
  docker compose -f "${COMPOSE_FILE}" "$@"
}

chain_a() {
  dc exec -T chain-a crossrefd --home /var/crossref "$@"
}

chain_b() {
  dc exec -T chain-b crossrefd --home /var/crossref "$@"
}

chain_c() {
  dc exec -T chain-c crossrefd --home /var/crossref "$@"
}

chain_d() {
  dc exec -T chain-d crossrefd --home /var/crossref "$@"
}

chain_e() {
  dc exec -T chain-e crossrefd --home /var/crossref "$@"
}

relayer() {
  dc exec -T relayer hermes "$@"
}

chain_id() {
  case "$1" in
    chain-a) printf '%s\n' "${CHAIN_A_ID}" ;;
    chain-b) printf '%s\n' "${CHAIN_B_ID}" ;;
    chain-c) printf '%s\n' "${CHAIN_C_ID}" ;;
    chain-d) printf '%s\n' "${CHAIN_D_ID}" ;;
    chain-e) printf '%s\n' "${CHAIN_E_ID}" ;;
    *) echo "unknown domain: $1" >&2; return 1 ;;
  esac
}

query_chain() {
  domain="$1"
  shift

  case "${domain}" in
    chain-a) chain_a "$@" ;;
    chain-b) chain_b "$@" ;;
    chain-c) chain_c "$@" ;;
    chain-d) chain_d "$@" ;;
    chain-e) chain_e "$@" ;;
    *) echo "unknown domain: ${domain}" >&2; return 1 ;;
  esac
}

tx_chain() {
  domain="$1"
  shift

  query_chain "${domain}" tx crossref "$@" --from validator --chain-id "$(chain_id "${domain}")" --keyring-backend test --yes --fees "0${DENOM}"
}

wait_tx() {
  domain="$1"
  txhash="$2"
  attempt=1

  while [ "${attempt}" -le 30 ]; do
    if out="$(query_chain "${domain}" query tx "${txhash}" --output json 2>&1)"; then
      printf '%s\n' "${out}"
      if printf '%s\n' "${out}" | grep -Eq '"code"[[:space:]]*:[[:space:]]*"?0"?'; then
        return 0
      fi
      echo "Transaction ${txhash} on ${domain} was included with a non-zero code." >&2
      return 1
    fi

    sleep 2
    attempt=$((attempt + 1))
  done

  echo "Timed out waiting for transaction ${txhash} on ${domain}." >&2
  return 1
}

run_tx() {
  domain="$1"
  shift

  out="$(tx_chain "${domain}" "$@" --output json 2>&1)"
  printf '%s\n' "${out}"
  txhash="$(printf '%s\n' "${out}" | sed -n 's/.*"txhash"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | tail -1)"
  if [ -z "${txhash}" ]; then
    echo "Could not parse ${domain} txhash." >&2
    return 1
  fi
  wait_tx "${domain}" "${txhash}"
}

channel_id() {
  local_domain="$1"
  remote_domain="$2"

  case "${local_domain}:${remote_domain}" in
    chain-a:chain-b) echo channel-0 ;;
    chain-a:chain-c) echo channel-1 ;;
    chain-a:chain-d) echo channel-2 ;;
    chain-a:chain-e) echo channel-3 ;;
    chain-b:chain-a) echo channel-0 ;;
    chain-b:chain-c) echo channel-1 ;;
    chain-b:chain-d) echo channel-2 ;;
    chain-b:chain-e) echo channel-3 ;;
    chain-c:chain-a) echo channel-0 ;;
    chain-c:chain-b) echo channel-1 ;;
    chain-c:chain-d) echo channel-2 ;;
    chain-c:chain-e) echo channel-3 ;;
    chain-d:chain-a) echo channel-0 ;;
    chain-d:chain-b) echo channel-1 ;;
    chain-d:chain-c) echo channel-2 ;;
    chain-d:chain-e) echo channel-3 ;;
    chain-e:chain-a) echo channel-0 ;;
    chain-e:chain-b) echo channel-1 ;;
    chain-e:chain-c) echo channel-2 ;;
    chain-e:chain-d) echo channel-3 ;;
    *) echo "unknown channel pair: ${local_domain}:${remote_domain}" >&2; return 1 ;;
  esac
}

route_id() {
  local_domain="$1"
  remote_domain="$2"

  case "${local_domain}:${remote_domain}" in
    chain-a:chain-b) echo route-00 ;;
    chain-b:chain-a) echo route-01 ;;
    chain-a:chain-c) echo route-02 ;;
    chain-c:chain-a) echo route-03 ;;
    chain-a:chain-d) echo route-04 ;;
    chain-d:chain-a) echo route-05 ;;
    chain-a:chain-e) echo route-06 ;;
    chain-e:chain-a) echo route-07 ;;
    chain-b:chain-c) echo route-08 ;;
    chain-c:chain-b) echo route-09 ;;
    chain-b:chain-d) echo route-10 ;;
    chain-d:chain-b) echo route-11 ;;
    chain-b:chain-e) echo route-12 ;;
    chain-e:chain-b) echo route-13 ;;
    chain-c:chain-d) echo route-14 ;;
    chain-d:chain-c) echo route-15 ;;
    chain-c:chain-e) echo route-16 ;;
    chain-e:chain-c) echo route-17 ;;
    chain-d:chain-e) echo route-18 ;;
    chain-e:chain-d) echo route-19 ;;
    *) echo "unknown route pair: ${local_domain}:${remote_domain}" >&2; return 1 ;;
  esac
}

client_for_source() {
  host_domain="$1"
  source_domain="$2"

  case "${host_domain}:${source_domain}" in
    chain-a:chain-b) echo 07-tendermint-0 ;;
    chain-a:chain-c) echo 07-tendermint-1 ;;
    chain-a:chain-d) echo 07-tendermint-2 ;;
    chain-a:chain-e) echo 07-tendermint-3 ;;
    chain-b:chain-a) echo 07-tendermint-0 ;;
    chain-b:chain-c) echo 07-tendermint-1 ;;
    chain-b:chain-d) echo 07-tendermint-2 ;;
    chain-b:chain-e) echo 07-tendermint-3 ;;
    chain-c:chain-a) echo 07-tendermint-0 ;;
    chain-c:chain-b) echo 07-tendermint-1 ;;
    chain-c:chain-d) echo 07-tendermint-2 ;;
    chain-c:chain-e) echo 07-tendermint-3 ;;
    chain-d:chain-a) echo 07-tendermint-0 ;;
    chain-d:chain-b) echo 07-tendermint-1 ;;
    chain-d:chain-c) echo 07-tendermint-2 ;;
    chain-d:chain-e) echo 07-tendermint-3 ;;
    chain-e:chain-a) echo 07-tendermint-0 ;;
    chain-e:chain-b) echo 07-tendermint-1 ;;
    chain-e:chain-c) echo 07-tendermint-2 ;;
    chain-e:chain-d) echo 07-tendermint-3 ;;
    *) echo "unknown client pair: ${host_domain}:${source_domain}" >&2; return 1 ;;
  esac
}

wait_channel() {
  domain="$1"
  channel="$2"
  route="$3"
  label="$4"

  echo "Waiting for ${route} ${label} actual=${domain}/${channel}..."
  until query_chain "${domain}" query ibc channel channels --output json | grep -q "${channel}"; do
    sleep 3
  done
}

wait_reference() {
  local_domain="$1"
  remote_domain="$2"
  height="$3"
  route="$(route_id "${remote_domain}" "${local_domain}")"

  echo "${route} ${local_domain} stored cross-reference for ${remote_domain}:"
  for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
    if query_chain "${local_domain}" query crossref cross-reference "${local_domain}" "${remote_domain}" "${height}" --output json; then
      return 0
    fi
    echo "Cross-reference ${local_domain}<-${remote_domain} not visible yet; retry ${attempt}/12..."
    sleep 5
  done

  query_chain "${local_domain}" query crossref cross-reference "${local_domain}" "${remote_domain}" "${height}" --output json
}

json_string_field() {
  key="$1"
  sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" | tail -1
}

json_number_field() {
  key="$1"
  sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p" | tail -1
}

checkpoint_proof_json() {
  domain="$1"
  checkpoint_height="$2"

  query_chain "${domain}" query crossref checkpoint-proof "${domain}" "${checkpoint_height}" --output json
}

hysteresis_seed() {
  domain="$1"
  echo "crossref-five-chain-hysteresis-${domain}"
}

hysteresis_json() {
  domain="$1"
  height="$2"
  block_hash="$3"
  app_hash="$4"

  go run docker/scripts/hysteresis-sign.go "${domain}" "${height}" "${block_hash}" "${app_hash}" "${BLOCK_TIME_UNIX}" "$(hysteresis_seed "${domain}")"
}

hysteresis_public_key() {
  domain="$1"
  json="$(hysteresis_json "${domain}" 1 YmxvY2stdGVtcGxhdGU= YXBwLXRlbXBsYXRl)"
  printf '%s\n' "${json}" | json_string_field public_key
}

public_key_for_domain() {
  domain="$1"

  case "${domain}" in
    chain-a) echo "${chain_a_public_key}" ;;
    chain-b) echo "${chain_b_public_key}" ;;
    chain-c) echo "${chain_c_public_key}" ;;
    chain-d) echo "${chain_d_public_key}" ;;
    chain-e) echo "${chain_e_public_key}" ;;
    *) echo "unknown domain: ${domain}" >&2; return 1 ;;
  esac
}

hysteresis_signature() {
  domain="$1"
  height="$2"
  block_hash="$3"
  app_hash="$4"

  json="$(hysteresis_json "${domain}" "${height}" "${block_hash}" "${app_hash}")"
  printf '%s\n' "${json}" | json_string_field signature
}

update_client_to_proof_height() {
  host_domain="$1"
  source_domain="$2"
  revision_height="$3"
  host_chain_id="$(chain_id "${host_domain}")"
  client_id="$(client_for_source "${host_domain}" "${source_domain}")"
  route="$(route_id "${source_domain}" "${host_domain}")"

  echo "Updating ${route} ${source_domain} -> ${host_domain} light client ${client_id} on ${host_chain_id} to proof height ${revision_height}..."
  relayer update client --host-chain "${host_chain_id}" --client "${client_id}" --height "${revision_height}"
}

require_proof_fields() {
  domain="$1"
  proof="$2"
  revision_number="$3"
  revision_height="$4"

  if [ -z "${proof}" ] || [ -z "${revision_number}" ] || [ -z "${revision_height}" ]; then
    echo "Could not parse ${domain} checkpoint proof." >&2
    exit 1
  fi
}

DOMAINS="chain-a chain-b chain-c chain-d chain-e"
PAIRS="chain-a:chain-b chain-a:chain-c chain-a:chain-d chain-a:chain-e chain-b:chain-c chain-b:chain-d chain-b:chain-e chain-c:chain-d chain-c:chain-e chain-d:chain-e"

echo "Preparing deterministic Ed25519 hysteresis public keys..."
chain_a_public_key="$(hysteresis_public_key chain-a)"
chain_b_public_key="$(hysteresis_public_key chain-b)"
chain_c_public_key="$(hysteresis_public_key chain-c)"
chain_d_public_key="$(hysteresis_public_key chain-d)"
chain_e_public_key="$(hysteresis_public_key chain-e)"

echo "Directed route numbering:"
for pair in ${PAIRS}; do
  left="${pair%%:*}"
  right="${pair##*:}"
  left_channel="$(channel_id "${left}" "${right}")"
  right_channel="$(channel_id "${right}" "${left}")"
  echo "  $(route_id "${left}" "${right}") ${left}/${left_channel} -> ${right}/${right_channel}"
  echo "  $(route_id "${right}" "${left}") ${right}/${right_channel} -> ${left}/${left_channel}"
done

for pair in ${PAIRS}; do
  left="${pair%%:*}"
  right="${pair##*:}"
  wait_channel "${left}" "$(channel_id "${left}" "${right}")" "$(route_id "${left}" "${right}")" "${left} -> ${right} channel"
  wait_channel "${right}" "$(channel_id "${right}" "${left}")" "$(route_id "${right}" "${left}")" "${right} -> ${left} channel"
done

echo "Registering all domains on all chains..."
for local_domain in ${DOMAINS}; do
  for remote_domain in ${DOMAINS}; do
    remote_public_key="$(public_key_for_domain "${remote_domain}")"
    echo "Registering ${remote_domain} on ${local_domain} with hysteresis public key ${remote_public_key}"
    run_tx "${local_domain}" register-domain validator "${remote_domain}" "$(chain_id "${remote_domain}")" --hysteresis-public-key "${remote_public_key}"
  done
done

echo "Binding crossref domains to five-chain IBC channels..."
for pair in ${PAIRS}; do
  left="${pair%%:*}"
  right="${pair##*:}"
  echo "Binding $(route_id "${left}" "${right}") ${left} -> ${right} actual=${left}/$(channel_id "${left}" "${right}")"
  run_tx "${left}" bind-domain-channel validator "${left}" "${right}" crossref "$(channel_id "${left}" "${right}")"
  echo "Binding $(route_id "${right}" "${left}") ${right} -> ${left} actual=${right}/$(channel_id "${right}" "${left}")"
  run_tx "${right}" bind-domain-channel validator "${right}" "${left}" crossref "$(channel_id "${right}" "${left}")"
done

echo "Submitting checkpoints on all five chains..."
chain_a_signature="$(hysteresis_signature chain-a 1 YmxvY2stYS0x YXBwLWEtMQ==)"
chain_b_signature="$(hysteresis_signature chain-b 1 YmxvY2stYi0x YXBwLWItMQ==)"
chain_c_signature="$(hysteresis_signature chain-c 1 YmxvY2stYy0x YXBwLWMtMQ==)"
chain_d_signature="$(hysteresis_signature chain-d 1 YmxvY2stZC0x YXBwLWQtMQ==)"
chain_e_signature="$(hysteresis_signature chain-e 1 YmxvY2stZS0x YXBwLWUtMQ==)"

run_tx chain-a submit-checkpoint validator chain-a 1 YmxvY2stYS0x YXBwLWEtMQ== --hysteresis-signature "${chain_a_signature}" --block-time-unix "${BLOCK_TIME_UNIX}"
run_tx chain-b submit-checkpoint validator chain-b 1 YmxvY2stYi0x YXBwLWItMQ== --hysteresis-signature "${chain_b_signature}" --block-time-unix "${BLOCK_TIME_UNIX}"
run_tx chain-c submit-checkpoint validator chain-c 1 YmxvY2stYy0x YXBwLWMtMQ== --hysteresis-signature "${chain_c_signature}" --block-time-unix "${BLOCK_TIME_UNIX}"
run_tx chain-d submit-checkpoint validator chain-d 1 YmxvY2stZC0x YXBwLWQtMQ== --hysteresis-signature "${chain_d_signature}" --block-time-unix "${BLOCK_TIME_UNIX}"
run_tx chain-e submit-checkpoint validator chain-e 1 YmxvY2stZS0x YXBwLWUtMQ== --hysteresis-signature "${chain_e_signature}" --block-time-unix "${BLOCK_TIME_UNIX}"

echo "Collecting checkpoint ICS23 proofs from source chains..."
chain_a_proof_json="$(checkpoint_proof_json chain-a 1)"
printf '%s\n' "${chain_a_proof_json}"
chain_a_proof="$(printf '%s\n' "${chain_a_proof_json}" | json_string_field source_checkpoint_proof)"
chain_a_proof_revision_number="$(printf '%s\n' "${chain_a_proof_json}" | json_number_field source_proof_revision_number)"
chain_a_proof_revision_height="$(printf '%s\n' "${chain_a_proof_json}" | json_number_field source_proof_revision_height)"
require_proof_fields chain-a "${chain_a_proof}" "${chain_a_proof_revision_number}" "${chain_a_proof_revision_height}"

chain_b_proof_json="$(checkpoint_proof_json chain-b 1)"
printf '%s\n' "${chain_b_proof_json}"
chain_b_proof="$(printf '%s\n' "${chain_b_proof_json}" | json_string_field source_checkpoint_proof)"
chain_b_proof_revision_number="$(printf '%s\n' "${chain_b_proof_json}" | json_number_field source_proof_revision_number)"
chain_b_proof_revision_height="$(printf '%s\n' "${chain_b_proof_json}" | json_number_field source_proof_revision_height)"
require_proof_fields chain-b "${chain_b_proof}" "${chain_b_proof_revision_number}" "${chain_b_proof_revision_height}"

chain_c_proof_json="$(checkpoint_proof_json chain-c 1)"
printf '%s\n' "${chain_c_proof_json}"
chain_c_proof="$(printf '%s\n' "${chain_c_proof_json}" | json_string_field source_checkpoint_proof)"
chain_c_proof_revision_number="$(printf '%s\n' "${chain_c_proof_json}" | json_number_field source_proof_revision_number)"
chain_c_proof_revision_height="$(printf '%s\n' "${chain_c_proof_json}" | json_number_field source_proof_revision_height)"
require_proof_fields chain-c "${chain_c_proof}" "${chain_c_proof_revision_number}" "${chain_c_proof_revision_height}"

chain_d_proof_json="$(checkpoint_proof_json chain-d 1)"
printf '%s\n' "${chain_d_proof_json}"
chain_d_proof="$(printf '%s\n' "${chain_d_proof_json}" | json_string_field source_checkpoint_proof)"
chain_d_proof_revision_number="$(printf '%s\n' "${chain_d_proof_json}" | json_number_field source_proof_revision_number)"
chain_d_proof_revision_height="$(printf '%s\n' "${chain_d_proof_json}" | json_number_field source_proof_revision_height)"
require_proof_fields chain-d "${chain_d_proof}" "${chain_d_proof_revision_number}" "${chain_d_proof_revision_height}"

chain_e_proof_json="$(checkpoint_proof_json chain-e 1)"
printf '%s\n' "${chain_e_proof_json}"
chain_e_proof="$(printf '%s\n' "${chain_e_proof_json}" | json_string_field source_checkpoint_proof)"
chain_e_proof_revision_number="$(printf '%s\n' "${chain_e_proof_json}" | json_number_field source_proof_revision_number)"
chain_e_proof_revision_height="$(printf '%s\n' "${chain_e_proof_json}" | json_number_field source_proof_revision_height)"
require_proof_fields chain-e "${chain_e_proof}" "${chain_e_proof_revision_number}" "${chain_e_proof_revision_height}"

echo "Updating destination light clients to source proof heights..."
update_client_to_proof_height chain-b chain-a "${chain_a_proof_revision_height}"
update_client_to_proof_height chain-c chain-a "${chain_a_proof_revision_height}"
update_client_to_proof_height chain-d chain-a "${chain_a_proof_revision_height}"
update_client_to_proof_height chain-e chain-a "${chain_a_proof_revision_height}"

update_client_to_proof_height chain-a chain-b "${chain_b_proof_revision_height}"
update_client_to_proof_height chain-c chain-b "${chain_b_proof_revision_height}"
update_client_to_proof_height chain-d chain-b "${chain_b_proof_revision_height}"
update_client_to_proof_height chain-e chain-b "${chain_b_proof_revision_height}"

update_client_to_proof_height chain-a chain-c "${chain_c_proof_revision_height}"
update_client_to_proof_height chain-b chain-c "${chain_c_proof_revision_height}"
update_client_to_proof_height chain-d chain-c "${chain_c_proof_revision_height}"
update_client_to_proof_height chain-e chain-c "${chain_c_proof_revision_height}"

update_client_to_proof_height chain-a chain-d "${chain_d_proof_revision_height}"
update_client_to_proof_height chain-b chain-d "${chain_d_proof_revision_height}"
update_client_to_proof_height chain-c chain-d "${chain_d_proof_revision_height}"
update_client_to_proof_height chain-e chain-d "${chain_d_proof_revision_height}"

update_client_to_proof_height chain-a chain-e "${chain_e_proof_revision_height}"
update_client_to_proof_height chain-b chain-e "${chain_e_proof_revision_height}"
update_client_to_proof_height chain-c chain-e "${chain_e_proof_revision_height}"
update_client_to_proof_height chain-d chain-e "${chain_e_proof_revision_height}"

echo "Broadcasting cross-reference packets from all five chains..."
run_tx chain-a broadcast-cross-reference-packet validator chain-a 1 \
  --source-checkpoint-proof "${chain_a_proof}" \
  --source-proof-revision-number "${chain_a_proof_revision_number}" \
  --source-proof-revision-height "${chain_a_proof_revision_height}"
run_tx chain-b broadcast-cross-reference-packet validator chain-b 1 \
  --source-checkpoint-proof "${chain_b_proof}" \
  --source-proof-revision-number "${chain_b_proof_revision_number}" \
  --source-proof-revision-height "${chain_b_proof_revision_height}"
run_tx chain-c broadcast-cross-reference-packet validator chain-c 1 \
  --source-checkpoint-proof "${chain_c_proof}" \
  --source-proof-revision-number "${chain_c_proof_revision_number}" \
  --source-proof-revision-height "${chain_c_proof_revision_height}"
run_tx chain-d broadcast-cross-reference-packet validator chain-d 1 \
  --source-checkpoint-proof "${chain_d_proof}" \
  --source-proof-revision-number "${chain_d_proof_revision_number}" \
  --source-proof-revision-height "${chain_d_proof_revision_height}"
run_tx chain-e broadcast-cross-reference-packet validator chain-e 1 \
  --source-checkpoint-proof "${chain_e_proof}" \
  --source-proof-revision-number "${chain_e_proof_revision_number}" \
  --source-proof-revision-height "${chain_e_proof_revision_height}"

sleep 20

echo "Verifying directed cross-references on all destination chains..."
for local_domain in ${DOMAINS}; do
  for remote_domain in ${DOMAINS}; do
    if [ "${local_domain}" != "${remote_domain}" ]; then
      wait_reference "${local_domain}" "${remote_domain}" 1
    fi
  done
done

echo "Five-chain cross-reference experiment passed."
