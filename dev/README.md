# Development Utilities

This directory contains off-chain development utilities for the sanction PoC.
These tools are intentionally kept outside the on-chain Cosmos SDK module.

## Layout

- `agent/`: local validator-bound sanction agent CLI and clients.
- `mock/`: mock risk, local LLM-compatible, and deterministic agent API
  services for development.
- `scripts/`: evaluation and demo scripts.

The on-chain implementation should contain only chain code such as proto
definitions, keepers, modules, and app wiring.

## Mock Agent API

`mock/agent-api` exposes a local `/v1/agents/evaluate` endpoint. It is a
deterministic placeholder for AI-agent judgment and is useful for repeatable
tests before connecting a real local LLM. The evaluator calibrates risk with
category severity, exposure type, amount, trusted or blocked addresses, and
evidence completeness.

```bash
go run ./dev/mock/agent-api -listen 127.0.0.1:8083
```

The scripted evaluation covers low-risk rejection, transaction blocking,
address-target compensating transfers, trusted-address downgrade and
blocked-address monitoring. A transaction target always maps to `block_tx`
at most, matching the on-chain policy, even when marked confirmed.

```bash
bash dev/scripts/evaluate-agent-api.sh
```

`agent/cmd/sanction-agent` can call `mock/agent-api` with `-agent-api-url`.
The decision is then used to calibrate the submitted risk score and is committed
to the on-chain audit hashes in the generated messages.
The local E2E also broadcasts signed Agent API decisions and compares the
submitted evidence and rationale hashes with the queried chain state.
It stops/restarts the risk service and checks that a watch agent stays alive
and broadcasts two sanctions with consecutive sequences. It verifies submission
and case state; the separate suppression step checks an already sanctioned tx.

```bash
bash dev/scripts/evaluate-agent-flow.sh
```

## SSH Server Experiments

Use `scripts/ssh-server-experiment.sh` from the local machine to sync the repo
to an SSH server, run remote setup checks, execute an experiment, and fetch
logs.

```bash
bash dev/scripts/ssh-server-experiment.sh \
  --host <ssh-host> \
  --user <ssh-user> \
  --experiment agent-flow
```

Remote-side helpers:

- `scripts/ssh-remote-setup.sh`: dependency checks and optional apt package
  installation.
- `scripts/ssh-remote-run.sh`: runs `checks`, `agent-flow`, `local-e2e`,
  `five-validator`, `five-agent`, `sanction-safety`, `agent-recovery`, `validator-lifecycle`, or `all`.
- `scripts/test-ssh-helpers.sh`: offline regressions for the Go version check,
  failure summaries, and log collection after failed experiments.

The Go requirement comes from `go.mod` (currently 1.25.10). Services run as
compiled binaries so cleanup and restart tests control the actual process.
Validator lifecycle tests use five nodes and require usable agent public keys.

On 2026-10-23, `revalidate-five-1` and `revalidate-lifecycle-1` both passed after
removing about 4.5 GiB of unused standard Go build cache. Both runs include the
full Go suite and shell syntax checks. The five-node run verified unanimous
revert approval, threshold transaction suppression and sanction persistence
across full restart. Lifecycle checks verified five registered validators,
automatic jail/unbond synchronization, and three remaining active agents.
These are local mock-service experiments; summaries and metrics remain under
the selected result root, separate from the earlier failed runs.

When space is limited, inspect `go env GOCACHE` and clear only an unused build
cache with `GOCACHE=/path/to/unused/cache go clean -cache`. Keep the cache used
by the experiment and do not remove dependency modules, node keys, chain state
or result logs to reclaim build space.

Legacy `-watch` mode screens all MsgSend messages and coins, retries transient failures,
refreshes evidence heights and advances the sequence after CheckTx acceptance.
Supply the current account number and sequence on startup. Multiple processes
sharing a key and recovery of lost broadcast acknowledgements still require
operator reconciliation.

## Durable Autonomous Daemon

`sanction-agent -daemon -config agent1.json -state-dir <persistent-dir>` enables
automatic proposals, independent voting, quorum/unanimity checks and execution.
Use distinct registered keys and state directories for all five resident agents.
The daemon reads chain policy and the active registry, persists observations and
review evidence, scans committed blocks on recovery, and confirms tx delivery.
`-target-type address` uses successful original bank sends for compensation;
pending `tx` blocking remains best effort.

`agent/internal/txmanager` uses synchronous bbolt transactions and an exclusive
DB lock. Signed bytes and hashes are persisted before broadcast; each account
has one in-flight intent, with automatic account/sequence queries and backoff.
Restart reconciles hash/bytes, DeliverTx result and height-pinned semantic state.
Case IDs and logical intent IDs are stable across restarts; the on-chain
execution-record guard protects the same case from duplicate effects. Monetary
actions also check the original-transfer settlement ledger across different cases.
The private key is not stored in this journal. `-daemon-status -state-dir ...`
exports a stopped journal. Keep one journal owner per account and retain the DB;
independent directories/hosts are fenced by on-chain signing-account leases. Trusted indexed RPC and
retained blocks/results are required. See the recovery commands below for
expired evidence, allowlisted failed retries and restoration. Missing backups,
chain resets and unsupported failures still require operator intervention.

```bash
bash dev/scripts/run-five-agent-e2e.sh
# Or include checks, metadata and a summary:
bash dev/scripts/ssh-remote-run.sh --experiment five-agent --result-root ./ssh-results
```

The fixture starts five validators, five agents and central mock services. A
test-only RPC proxy withholds one accepted execute response, then all agents
are SIGKILLed and restarted from disk. It processes two original transfers,
checks five distinct votes per case, one case per original hash, exactly two
successful execution events, unchanged sender/recipient balances after return,
and one broadcast attempt for the interrupted execute. Journals and metrics are
saved under the fixture home. The scenario runs in the PR five-validator job;
CI paths use Linux `/tmp` directories rather than macOS-specific paths.

Unit regressions additionally cover a crash between signing and broadcast,
unknown lookup failures, external sequence consumption, permanent failures,
exclusive journal ownership, replay before cursor checkpoint, mismatched
original-transfer evidence and malformed indexed tx responses.

Agent and transaction signing share strict secp256k1 scalar validation.
The broadcast client checks JSON-RPC version/request ID, an explicit CheckTx
code and the submitted tx hash before reporting success. The watch regression
injects a mismatched hash and checks sequences `7, 7, 8`: retry the same sequence
after an invalid acknowledgement, then advance after a valid one.

App integration regressions exercise settlement from frozen accounts using the
real bank keeper. Five-agent unanimous revert and four-of-five approved escrow
preserve freezes and keep ordinary transfers blocked. Escrow uses a seeded case;
its selection by the default risk policy is not implemented.

## Operator Helpers

- `agent/cmd/g503-ops`: config template generation, new key generation, false-positive
  audit bundle creation, and signed `MsgRevokeSanction` broadcast.
- `agent/cmd/g503-dashboard`: read-only local dashboard for sanction params and
  agent registry state.

Generating a new key does not update the on-chain registry. New node genesis
configures a separate sanction authority (`demo-keys.json` / `authority`).
`review-sanction -evidence-hash-hex <SHA256>` records its signed, accepted
false-positive review; only then can `revoke-sanction` lift that case's sanctions.
Audit bundles embed evidence bytes and hash canonical JSON. Public appeal intake,
rejected-review workflows, governance and identity-bound RBAC remain future work.

## Settlement Safety E2E

```bash
bash dev/scripts/run-sanction-safety-e2e.sh
bash dev/scripts/ssh-remote-run.sh --experiment sanction-safety --result-root ./ssh-results
```

The real five-node fixture first runs autonomous crash recovery, then tests two
fully five-signature-approved cases against the same original transfer, forged
original metadata, automatic five-agent freeze, authority-reviewed revoke,
ordinary sending after revoke, and all-node restart persistence. Expected failed
txs must reach DeliverTx with the specific sanction error, not merely fail in
CheckTx. Evidence and `safety-metrics.json` are retained in `logs/`.

`dev/agent/e2e` tests skip unless launched by this fixture. The post-handler
records only successful whole `bank.MsgSend` transactions. Revert and escrow
share a receipt-keyed ledger `(tx hash, message_index, denom)`, checked at both
case opening and execution and preserved after revoke. Pre-upgrade transfers
without receipts fail closed. This requires explicit migration for old chains;
it does not retroactively prove old transactions or roll back finalized history.

With the Go/grpc/gateway protobuf plugins on `PATH`, regenerate the module with
`buf generate proto --path proto/sanction/v1`. Limit generation to this path so
the vendored SDK/Google import definitions are not emitted into this module.

## Agent Disaster Recovery

```bash
bash dev/scripts/run-agent-recovery-e2e.sh
bash dev/scripts/ssh-remote-run.sh --experiment agent-recovery --result-root ./ssh-results
```

This adds offline backup/restore during lost execute acknowledgement, five gas
failures, expiry during downtime, repaired-gas explicit retry, stale backup
reconciliation without sequence changes, and expired-case replacement with five
new votes. `logs/recovery-metrics.json` records actual successful events and
zero duplicates. Risk/LLM/Agent API services remain mock implementations.

Use `sanction-agent -daemon -config agent1.json -target-type address
-retry-intent <ID> -retry-reason <REASON> -retry-request-id <UNIQUE_REPAIR_ID>`
after fixing an allowlisted failure. Old attempts/messages remain auditable;
the same request ID cannot reapply a repair after restart. Uncertain outcomes
and unsupported errors fail closed. Expired cached reviews invoke the services
again. An expired address case gets an independently voted `-rN` successor,
linked by `previous_case_id`; final decisions and executed sanctions are never
revived. Cross-case monetary dedup remains enforced on-chain.

Offline `-journal-backup <ARCHIVE>` creates a `0600` tar snapshot, never
overwrites, and refuses a live DB. Restore with `-journal-restore <ARCHIVE>
-backup-sha256 <TRUSTED_SHA256> -journal-genesis-hash <TRUSTED_GENESIS_HASH>`
into a new `-state-dir`, with the same config/key/`-target-type address`.
The key file is not in the archive. Keep checksums and keys separately secure;
archives contain evidence and signed txs and are not encrypted or signed.
The size cap is 128 MiB. Startup validates the RPC genesis fingerprint; legacy
journals require explicit trusted binding. `-print-genesis-hash` queries the RPC
without reading a key. Restore does not waive the one-journal-owner-per-key rule,
replace retained block/index data, repair arbitrary corruption or reset a chain.
See the main READMEs for complete command examples and migration limitations.

## Ownership and Voter Snapshot E2E

`bash scripts/run-ownership-snapshot-e2e.sh` (from this directory) starts with five
bonded validators/five agents, adds a sixth validator during an existing five-voter
case, jails node5, unbonds node3 and restores stopped-journal backups into independent
state directories. It checks duplicate startup refusal, higher fencing epochs,
five resumed old processes exiting without send, ten missing/stale memo rejections,
unchanged old-case voters/power, four voters in a new case, one settlement and
all-six-node restart. `logs/ownership-metrics.json` records results. SSH experiment
`ownership-snapshot`, `all` and PR CI invoke it.

The old hosts first produce four automatic independent votes while agent5 is
paused. Restored agents supply the fifth vote and execute. All five signatures
are verified against the frozen snapshot; the old hosts' send counts cannot
increase after resume, and a fresh restart of the old host is also refused.
This fixture raises evidence TTL to 300 blocks and voting period to 100 through
an authority-signed parameter update to keep the multi-phase test from expiring
the case accidentally; ordinary defaults are unchanged.

The daemon acquires ownership before work and validates before every broadcast.
`-lease-ttl-blocks` defaults to 24 (6..100); `-lease-wait 90s` explicitly enables a
planned takeover wait. Without it, live ownership rejects startup. Never reuse a
session ID from a journal. Lease transactions consume sequence, so recovery tests
subtract confirmed acquisition/renewal events before asserting zero business resends.
All transactions from a previously leased account require its signed ownership
memo, including legacy CLI calls. This is cooperative fencing, not key-theft
protection. Height-based ownership cannot fail over during a chain halt.

Case snapshots fix signer keys, weights, quorum and unanimity at opening. Historical
members remain eligible after jail/unbond; they are not included in new cases.
An absent member never silently reduces an old denominator. Missing snapshots
require a deliberate historical-state migration and are never reconstructed from
the current registry. This fixture emulates hosts with local processes/directories;
actual multi-host/network partition validation and real risk/LLM services remain
separate work.

## Protobuf APIs and Journal Indexes

Sanction protobuf APIs now take/return pointers. Signers return independent
`proto.Clone` copies; do not dereference a protobuf message to copy it. Standard
`go vet ./...` must pass without disabling `copylocks`. CI and the SSH runner's
`checks` include standard vet, the complete Go suite and `go test -race ./...`.

Daemon ticks and txmanager steps read only `active_work` and `active_intents`
secondary indexes. Writes update primary data and indexes atomically. Failed
intents and expired work remain eligible for the existing recovery flow;
confirmed/satisfied/cancelled intents and completed work remain in the audit
history but are not scanned on each tick. Ordering remains primary-key order.

An old journal builds version `1` indexes once on open. Migration failure rolls
back, a missing known-version index is rebuilt and unknown versions are rejected.
Legacy backups restore normally and migrate on open; new backups also validate
index membership. Never write a migrated journal with an older binary. Keep an
offline backup before deployment and use a matching backup for rollback.
