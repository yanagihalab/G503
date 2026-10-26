# G503: Cosmos SDK AI Sanction Chain

G503 is a Cosmos SDK prototype for preventing high-risk crypto-asset transfers
through three steps:

1. anomaly detection using a risk-screening service such as Chainalysis address
   screening;
2. DAO-style consensus by validator-bound AI agents;
3. on-chain transaction suppression or sanction execution before or after
   finalization.

It contains only the sanction system implementation, its local AI-agent
development utilities, mock services, and design documents.

Japanese documentation is available in [README.ja.md](README.ja.md).

## Repository Layout

```text
docs/
  ai-sanction-system-design.md
  ai-sanction-system-spec.md
app/
  Cosmos SDK BaseApp scaffold
cmd/
  g503d/
proto/
  sanction/v1/
x/
  sanction/
dev/
  agent/
  mock/
  scripts/
```

## Core Idea

When a transfer appears to involve a scam, sanctioned entity, money laundering,
or another high-risk address, a user, node operator, or validator-bound agent can
submit a risk report. AI agents then vote on whether the transaction should be
watched, blocked, frozen, escrowed, or reverted.

If consensus is reached before finalization, validators suppress the transaction
from proposal processing. If execution is needed after approval, the chain can
execute a special sanction transaction that requires the agreed agent decision.

## On-Chain Module

The `x/sanction` module provides:

- agent registration;
- risk report submission;
- sanction case opening;
- AI-agent vote submission;
- sanction execution and revocation;
- active transaction sanctions used by `PrepareProposal` and `ProcessProposal`;
- query endpoints and genesis import/export.

The proto definitions live under `proto/sanction/v1`.

## Cosmos SDK Scaffold

`app/` and `cmd/g503d/` provide a lightweight Cosmos SDK scaffold for G503,
independent from crossref. It wires the `x/sanction` store, Msg/Query routing,
genesis handling, and `PrepareProposal` / `ProcessProposal` hooks into
`baseapp.BaseApp`. The scaffold also includes the minimum modules needed for
local signed transaction demos: `auth`, `bank`, `staking`,
`consensus`, and `slashing`.

```bash
go run ./cmd/g503d version
```

This is a research PoC scaffold, not a production chain template. Risk report
and agent vote authenticity are handled first by the signatures verified inside
`x/sanction`. Production use still requires hardened validator operations,
secure key management, policy governance, and abuse-resistance controls.

## Local Demo Milestones

The local demo path is designed to be reproducible end to end.

1. Start a local G503 chain.
2. Submit a normal transfer transaction.
3. Let the agent detect the pending transaction and run risk screening plus
   local LLM judgment.
4. Let the agent submit the sanction transaction.
5. Reproduce the full flow from detection to sanction execution.

The repository includes scripts for local E2E demos, five-validator sanction
flows, and validator lifecycle checks.

## Off-Chain Development Utilities

The `dev/` directory is intentionally off-chain:

- `dev/agent`: local AI-agent CLI and clients.
- `dev/mock/agent-api`: deterministic mock AI-agent API for mechanical policy
  evaluation.
- `dev/mock/risk-service`: mock Chainalysis-compatible risk service.
- `dev/mock/llm-service`: mock local LLM explanation service.
- `dev/scripts/evaluate-sanction-latency.sh`: latency evaluation helper.
- `dev/scripts/evaluate-agent-api.sh`: mock agent API evaluation helper.

The current AI-agent development mode assumes a centrally managed local LLM.
Future work can move toward distributed validator-operated LLM agents.

The agent can turn its risk/LLM result into a Cosmos SDK tx containing
`MsgSubmitRiskReport`, `MsgOpenSanctionCase`, and `MsgSubmitSanctionVote`, then
broadcast it through CometBFT RPC.

```bash
go run ./dev/agent/cmd/sanction-agent \
  -tx ./sample-tx.json \
  -agent-id validator-1 \
  -privkey-hex <32-byte-secp256k1-private-key-hex> \
  -broadcast \
  -rpc-url http://127.0.0.1:26657
```

In watch mode, the agent polls `/unconfirmed_txs`, decodes Cosmos SDK tx bytes,
screens every `cosmos.bank.v1beta1.MsgSend` and denomination, and selects the
strongest required action before broadcasting once per transaction. Transient
RPC or screening errors are logged and retried. Deduplication retains only the
current pending set.

```bash
go run ./dev/agent/cmd/sanction-agent \
  -watch \
  -agent-id validator-1 \
  -privkey-hex <32-byte-secp256k1-private-key-hex> \
  -broadcast \
  -rpc-url http://127.0.0.1:26657 \
  -chain-id g503-local-1 \
  -account-number <agent-account-number> \
  -sequence <current-agent-sequence>
```

Watch signing refreshes the observed block height and evidence expiry. Accepted
`broadcast_tx_sync` submissions advance the local sequence. Query the chain for
the account number and sequence when restarting a watcher; concurrent use of
the same signing key and reconciliation of lost broadcast responses are not
implemented. Watch output is JSON Lines; Ctrl-C and SIGTERM stop the process.

### Durable Autonomous Agents

Use `-daemon` for automatic proposal, independent voting, execution and committed
transaction confirmation. Run one process per registered agent/key, with a
different persistent state directory for each account:

```bash
go run ./dev/agent/cmd/sanction-agent \
  -config ./agent1.json -daemon -state-dir ./.g503/agent1 \
  -target-type address -poll-interval 500ms
```

The existing JSON config supplies chain ID, RPC, risk/LLM URLs, agent ID and an
owner-only `key_file`. It can also contain
`"daemon":{"enabled":true,"state_dir":"./.g503/agent1","start_height":0}`.
Repeat with the other four registered keys/configs and state directories.
Account number and sequence are read automatically; do not manage them by hand.

- A synchronous bbolt journal (`agent.db`, mode `0600`) stores observations,
  review evidence, the block cursor, intents and signed tx bytes **before** send.
  An exclusive DB lock prevents two processes from sharing that journal.
- Each agent reads the active registry and chain policy, reviews independently,
  and uses `auto-<UPPERCASE_TX_HASH>` as the common case ID. Staggered agent ranks
  prefer one proposer/executor while allowing another agent to take over.
- The daemon scans mempool and successful committed blocks. The first start
  begins after the current tip; `-start-height` enables explicit historical
  replay only for a new journal. Restart resumes the saved cursor instead.
- Signed bytes are retried with backoff. RPC failures are not treated as tx
  absence. The manager checks indexed tx bytes/hash, DeliverTx result and
  height-pinned case/vote/execution state before re-signing or advancing.
- Address-target cases require a successful original `bank.MsgSend` whose
  sender, recipient, amount and denom match the report. The demo uses confirmed
  transfer compensation, not a rollback of finalized chain history.

Inspect a stopped agent without connecting to the chain:

```bash
go run ./dev/agent/cmd/sanction-agent -daemon-status -state-dir ./.g503/agent1
```

Journal intent states are `queued`, `pending`, `confirmed`, `satisfied` (the
effect is already verified on-chain), `cancelled`, and `failed`. CheckTx alone
never marks an intent confirmed. Definite execution/gas/fee failures stop that
intent for operator review instead of retrying indefinitely.

Preserve the state directory on a persistent volume and use one journal owner
per signing account. Daemons now use on-chain ownership leases and fencing epochs
across independent directories and hosts. Automatic
recovery assumes trusted RPC, enabled tx indexing, retained blocks/results and
the same non-reset chain. Expired evidence, allowlisted failed-intent retry and
backup restoration are covered below. Lost journals without a backup, chain
resets and unsupported permanent failures still require operator intervention.
Unanimous revert waits for every voter frozen into that case's snapshot; executor failover never
relaxes the on-chain signature requirement.
Pending `-target-type tx` suppression is best effort and cannot undo a tx that
has already finalized; use the explicit address-target compensation workflow
for this demo. Legacy `-watch` retains its non-durable workflow; its RPC access
is now guarded as described below.

```bash
bash dev/scripts/run-five-agent-e2e.sh
```

This starts five validators and five resident agents with central mock risk,
LLM and Agent API services. A loopback fault proxy withholds one accepted
execute response; all five agents are SIGKILLed, restarted with their journals,
and process a second transfer. Assertions cover five distinct votes per case,
one case per original tx, exactly two successful execution events, exact balance
restoration, and no rebroadcast of the interrupted execute. Results include
`logs/autonomous-metrics.json` and stopped-agent journal snapshots. The test is
also available as `--experiment five-agent` in the SSH runner and in PR CI.

### Disaster Recovery

Expired unbroadcast evidence is independently re-screened through risk, LLM and
Agent API services, using current height and policy. This does not merely extend
the old expiry. TTL is the smaller of CLI `expiry-height - height` and on-chain
`evidence_ttl_blocks`, with a minimum of three blocks. Use the default 100 blocks
to leave adequate time for consensus.

An unexecuted address case with expired evidence or voting period is replaced by
`auto-<HASH>-r1`, then `-r2`, etc. The on-chain `previous_case_id` guard checks the
same transfer, a newer observation, expiry and a single successor. Old cases,
reports and votes remain auditable; votes are not copied into the new case.
Execution also rejects expired evidence. Executed, rejected and revoked cases
are not revived, and active freezes are not automatically lifted. This does not
extend pending-tx suppression beyond finality.

After repairing gas, fees or funding, explicitly retry a failed intent:

```bash
go run ./dev/agent/cmd/sanction-agent -config agent1.json -daemon -target-type address \
  -retry-intent 'open/auto-<HASH>' -retry-reason 'gas corrected after operator review' \
  -retry-request-id gas-repair-001
```

Recovery checks all previous hashes/results and height-pinned sequence and
case/vote/execution state before preparing another tx. An already completed
effect is reconciled without sending. RPC errors and unresolved outcomes fail
closed. Only out-of-gas, insufficient funds/fee, full mempool, wrong sequence and
expired evidence are eligible; authorization, signature and invalid-request
failures are not. Open/vote messages are re-evaluated and signed again; execution
also requires the current evaluation to match the approved action. Signed bytes,
attempts, old messages, reason, height and request ID remain in the journal.
Restarting the same request ID cannot repeat a repair or endlessly retry another
failure. If omitted, the ID is the reason's SHA-256. Use a new ID for another
operator repair. Only unbroadcast expired payloads and expired-report `open`
failures recover automatically after re-evaluation.

Journals are bound to a genesis fingerprint, not only a reusable chain ID.
This detects a different genesis, not every fork/reset using identical genesis;
continuity of the same finalized history remains a prerequisite:

```bash
go run ./dev/agent/cmd/sanction-agent -config agent1.json -print-genesis-hash
```

This reads no private key. Legacy journals without a fingerprint fail closed.
Only after verifying the original network's genesis, explicitly bind one with
`-journal-genesis-hash <TRUSTED_GENESIS_HASH>`. Do not bind a reset chain merely
because its chain ID matches.

Stop the agent before backing up or restoring. Live DB locks, existing output
files, identity/genesis mismatch, checksum mismatch and corrupt databases are
rejected:

```bash
go run ./dev/agent/cmd/sanction-agent -state-dir ./.g503/agent1 \
  -journal-backup ./backups/agent1-001.tar
# Keep the returned sha256 and manifest.genesis_hash in trusted separate storage.
# The destination must be new, and no other agent may use this signing key.
go run ./dev/agent/cmd/sanction-agent -config agent1.json -target-type address \
  -state-dir ./.g503/agent1-restored -journal-restore ./backups/agent1-001.tar \
  -backup-sha256 <TRUSTED_ARCHIVE_SHA256> -journal-genesis-hash <TRUSTED_GENESIS_HASH>
go run ./dev/agent/cmd/sanction-agent -config agent1.json -daemon -target-type address \
  -state-dir ./.g503/agent1-restored
```

The mode `0600` tar archive holds a consistent DB snapshot and manifest, capped
at 128 MiB, published with fsync and no overwrite. Restore verifies the inner
digest, DB structure and signed-attempt hashes. Checksums are not encryption or
signed-backup authentication; keep the trusted checksum separately. Archives
contain sensitive evidence and replayable signed txs. Key files are excluded
and require separate secure backup. Startup reconciles chain state before
sending. Online backups and automatic repair of damaged databases remain unsupported.

```bash
bash dev/scripts/run-agent-recovery-e2e.sh
```

The five-validator/five-agent fixture includes lost-execute-ack backup/restore,
gas failure on all agents, expired-evidence re-evaluation and explicit retry,
restoring stale pre-success backups without broadcasting, and a fresh five-vote
round after interrupted voting expires. It checks execution events, sequences
and balances, saving `logs/recovery-metrics.json`. These are local mock risk,
LLM and Agent API tests. PR CI and SSH `--experiment agent-recovery` / `all`
include the scenario.

Verification on 2026-10-23 passed the full `regression-1` run and recovery reruns
`final-1` / `final-2`. The latest run completed two recovery executions with no
duplicates, restored balances and unchanged sequences after stale failed-backup
restoration. Offline restores also passed with unreachable RPC and no Agent API
token. The complete Go suite and focused race tests passed. CI is configured;
these are local results, not evidence of a GitHub CI run.

### Signing Ownership and Fixed Voters

Every daemon startup generates a fresh random session and confirms a standalone
`MsgClaimAgentLease` for its signing account. Independent hosts coordinate through
finalized chain state, without shared files. A live owner causes immediate startup
rejection by default. `-lease-ttl-blocks` accepts 6..100 blocks (default 24), renewed
at half its remaining lifetime. Acquisition and renewal consume gas, fees and
account sequence like other signed transactions.

For a planned recovery, stop the old agent, restore its journal, and explicitly
wait for the old owner's block-height lease to expire:

```bash
go run ./dev/agent/cmd/sanction-agent -config agent1.json -daemon -target-type address \
  -state-dir ./.g503/agent1-restored -lease-ttl-blocks 24 -lease-wait 90s
```

Each acquisition increments a persistent epoch. Transactions sign the epoch and
session in their memo; ownership is checked every tick and immediately before
broadcast. An expired/replaced process exits without reacquiring. RPC errors stop
sends. CheckTx and DeliverTx both reject missing, expired or stale proofs, including
transactions previously admitted to a mempool. Once an account has a lease, ordinary
bank transactions and legacy/manual CLI transactions from that account also need
its current proof. Leases cannot be deleted to reset their epoch. Restored intents
are reconciled before re-signing attempts made unexecutable by an ownership change;
already successful effects are not repeated. Backups remain offline, and copied
journals never resurrect a process session.
`GET /sanction/v1/leases/{signer_address}` exposes the current holder, epoch and
expiry height for operational inspection.

This coordinates cooperative processes; it is not protection against someone with
a stolen private key, nor a KMS/HSM. The trusted, continuous chain remains required.
A halted chain cannot advance height-based expiry or authorize a replacement owner.

Case creation freezes sorted voter IDs, signing addresses, public keys and voting
powers, plus quorum and the unanimous-revert requirement. These records govern
signature verification, tally and execution. Added validators cannot vote on old
cases. Jail, unbond, power changes and parameter updates cannot lower their approval
requirements. Historical members retain eligibility for those cases after jail,
but are absent from new snapshots. Missing signatures block execution; after
evidence expiry, a separately evaluated replacement case snapshots the new set and
requires fresh votes. Queries, genesis export/import and restart preserve snapshots.
Legacy cases without snapshots fail closed: deployment to an existing chain needs
an explicit, historically justified upgrade/migration, not an implicit snapshot of
the current registry or a hot binary replacement.

```bash
bash dev/scripts/run-ownership-snapshot-e2e.sh
```

The fixture starts with five validators/five agents, rejects an independent duplicate
journal and backs up stopped journals. Agent5 is paused while the other four agents
detect the transfer, independently evaluate and vote. All old processes are then
paused; the fixture adds a sixth validator, jails node5 and unbonds node3.
Old/new cases retain five/four voters.
Four-signature revert and the added validator's old-case vote are rejected. Five
restored agents complete the fifth vote and execute; all five resumed old processes
stop without additional sends, and an old host's fresh restart is also rejected.
Ten missing/stale proof transactions are rejected, and all six nodes are
restarted to verify persistent snapshots and one settlement. Results go to
`logs/ownership-metrics.json`. PR CI and SSH `--experiment ownership-snapshot` / `all`
include this test. Independent local directories/processes emulate hosts: this is
not an actual multi-host SSH or production partition test, and all risk/AI services
are mocks.

An authority-signed parameter update gives this multi-phase fixture 300-block
evidence TTL and a 100-block voting period; ordinary chain defaults are unchanged.

The local `regression-final` (`all`) run on 2026-10-26 passed: five automatic votes,
zero additional sends from the five resumed old processes, old-host fresh restart
refusal, ten missing/stale proof rejections, one execution event, zero duplicate
settlements and restored balances. Existing single-node, five-validator, reviewed
revoke, recovery and lifecycle scenarios, the full Go suite and related race checks
also passed. CI configuration is updated; these are local results, not a GitHub CI run.

### Mock AI Agent API

`dev/mock/agent-api` is a deterministic stand-in for the AI-agent decision
endpoint. It is intentionally mechanical, but no longer returns a fixed answer:
it calibrates the raw risk score with category severity, direct or indirect
exposure, transfer amount, trusted or blocked addresses, and evidence
completeness.

The response includes:

- `raw_risk_score` and `calibrated_score`;
- `action` and `vote_option`;
- `review_level`;
- `evidence_status`;
- `findings`;
- `required_evidence`;
- deterministic `decision_id`.

Run the API locally:

```bash
go run ./dev/mock/agent-api \
  -listen 127.0.0.1:8083 \
  -api-token local-agent-token
```

Run the mechanical evaluation suite:

```bash
AGENT_API_TOKEN=local-agent-token \
  bash dev/scripts/evaluate-agent-api.sh
```

`sanction-agent` can also call this API before building on-chain messages. When
`-agent-api-url` is set, the returned `calibrated_score` is used as the
submitted `RiskReport.risk_score`. The original risk score/response ID, evaluated
risk, LLM explanation, and agent decision JSON are committed into audit hashes.
Signing requires `-key-file` or `-privkey-hex`; broadcasting additionally requires
`-broadcast` and the correct account number and sequence. Inconsistent decisions
or incomplete evidence stop signing. Transaction targets always use at most
`block_tx`, including confirmed transactions. The existing compensating-transfer
demo uses an address target; it does not erase finalized block history.

```bash
go run ./dev/agent/cmd/sanction-agent \
  -tx ./sample-tx.json \
  -agent-id validator-1 \
  -agent-api-url http://127.0.0.1:8083 \
  -agent-api-token "$G503_AGENT_API_TOKEN"
```

The integration path can be checked with:

```bash
bash dev/scripts/evaluate-agent-flow.sh
```

### SSH Server Experiment

The repository includes helpers for running experiments on a real SSH server.
The local wrapper syncs this repository with `rsync`, checks the remote
environment, runs the selected experiment, and fetches logs back to the local
workspace.

Remote requirements are Linux, Go `1.25.10` or newer (following `go.mod`),
`bash`, `curl`, `git`, `jq`, `perl`, `xxd`, and `rsync`.
On Ubuntu / Debian servers, `--install-deps` can install
common packages through `apt-get`; Go itself should be installed beforehand.

```bash
bash dev/scripts/ssh-server-experiment.sh \
  --host <ssh-host> \
  --user <ssh-user> \
  --identity ~/.ssh/id_ed25519 \
  --experiment agent-flow
```

Available experiments:

- `checks`: shell syntax, the complete Go suite, standard `go vet ./...` and full race tests;
- `agent-flow`: mock API evaluation plus signed report and vote generation;
- `local-e2e`: risk-service outage recovery, two automatic watch broadcasts,
  Agent API signing, on-chain audit hashes and suppression after sanction approval;
- `five-validator`: five-validator sanction E2E;
- `five-agent`: five autonomous agents, durable sends, lost-ack and SIGKILL recovery;
- `sanction-safety`: the above plus original-transfer verification, duplicate recovery rejection and reviewed revocation;
- `agent-recovery`: the above plus expired evidence, explicit failed retries and journal restoration;
- `validator-lifecycle`: five-validator registration, public keys, jail and
  unbond registry synchronization;
- `all`: run the main experiments sequentially.

Remote results are written to `g503-ssh-lab/ssh-results/<run-id>` and fetched
locally into `.g503/ssh-results/<host>-<run-id>`.
Failures also produce a summary with the failing step and exit code, and the
wrapper attempts log collection. Restart tests stop the actual node binaries,
check all five RPC endpoints are down, and verify sanction state on all five
nodes after restart.

Run helper regression tests without an SSH server:

```bash
bash dev/scripts/test-ssh-helpers.sh
```

### Operator Tools

`dev/agent/cmd/g503-ops` contains operator-oriented helpers:

- `config-template`: print a JSON config for `sanction-agent`;
- `rotate-key`: generate a new `0600` key file without overwriting existing keys;
- `audit-bundle`: preserve artifact bytes as base64 and compute a reproducible
  SHA-256 hash;
- `review-sanction`: sign and record an accepted false-positive review with an evidence hash;
- `revoke-sanction`: build and broadcast a signed `MsgRevokeSanction`.

Key generation does not update an existing agent's on-chain key. New `g503d init`
homes include a separate funded sanction authority, configured in
`app_state.sanction.authority`. Its research-only key is in the mode `0600`
`config/demo-keys.json` under `authority`, not production secret storage. This
authority controls only the sanction module; other SDK modules retain their
`gov` authority. Old genesis files without an authority retain the `gov` fallback.
The app still does not integrate `x/gov`.

### Original Transfers and Reviewed Revocation

An app post-handler records every coin of each successful `bank.MsgSend`, only
after the whole transaction succeeds. CheckTx, simulation and failed transactions
produce no receipts. Case opening and execution of revert/escrow verify the
previously committed receipt's hash, zero-based `message_index`, sender,
recipient, amount and denom. Omitted message indices mean zero.

The settlement key is `tx hash + message_index + denom`, independent of case ID
and shared by revert and escrow. Separate approved cases cannot recover the
same transfer twice. Failed bank operations do not consume the receipt; revoke
never deletes settlement history. Recovery is a compensating transfer, not a
rollback of finalized history. Transfers predating this receipt mechanism are
rejected rather than trusted retroactively. Existing chains need an explicit
upgrade/genesis migration; this is not a transparent hot upgrade.

Revocation requires two authority-signed transactions: `MsgReviewSanction` with
a reason and 32-byte evidence hash, followed by `MsgRevokeSanction`. An accepted
review is immutable, one per case, and alone does not lift a freeze. The chain
retains reviewer, evidence hash, reasons and review/revoke heights. This is an
operator attestation after review, not an AI guarantee of evidence authenticity.

```bash
go run ./dev/agent/cmd/g503-ops audit-bundle \
  -case case.json -votes votes.json -decision review-evidence.json -out review-bundle.json
go run ./dev/agent/cmd/g503-ops review-sanction \
  -rpc-url http://127.0.0.1:26657 -chain-id g503-local-1 \
  -key-file authority.key -case-id <CASE_ID> -reason 'false positive confirmed' \
  -evidence-hash-hex <BUNDLE_SHA256> -account-number <NUMBER> -sequence <SEQUENCE>
# Confirm review delivery first, then use the updated account sequence.
go run ./dev/agent/cmd/g503-ops revoke-sanction \
  -rpc-url http://127.0.0.1:26657 -chain-id g503-local-1 \
  -key-file authority.key -case-id <CASE_ID> -reason 'review accepted' \
  -account-number <NUMBER> -sequence <NEXT_SEQUENCE>
```

Queries: `/sanction/v1/authority`, `/sanction/v1/tx/{hash}/transfers`,
`/sanction/v1/tx/{hash}/settlements`, `/sanction/v1/cases/{id}/review`.
Receipts, settlements and review records survive genesis export/import and node
restarts. Public appeals, rejected-review workflows, compensation adjudication,
multisig/governance authority management remain future work.

```bash
bash dev/scripts/run-sanction-safety-e2e.sh
```

This includes autonomous five-agent crash recovery, then opens two cases before
recovery and approves each with five signatures. Only the first can execute.
Five resident agents then freeze an address automatically; tests reject
unauthorized revoke and revoke-before-review, attest review evidence, revoke
with the authority and resume an ordinary bank send.
Review evidence includes five signed no-sanction re-evaluations using corrected
mock risk and the central Agent API, accepted by the operator. These are preserved
evidence, not a replacement of authority authorization with a second on-chain vote.
All five nodes restart; each must retain the settlement and review, and duplicate recovery must still
fail. Logs include `safety-metrics.json`, signed transaction results and review
evidence. PR CI and the SSH runner (`--experiment sanction-safety`, also `all`)
include this scenario.

The Agent API supports a bearer token, a role-header allowlist, optional mTLS,
per-IP rate limits, body limits and HTTP timeouts. The role header is not
identity-bound production RBAC. A client CA requires a server TLS certificate
and key.

`dev/agent/cmd/g503-dashboard` is a small read-only local dashboard for REST
API status, currently showing sanction params and agent registry state.

## Test

```bash
go test -count=1 ./...
```

Cases bind reports to the same transaction, recipient and transfer metadata.
Regressions reject invalid vote options, nonpositive amounts and empty-set
unanimity. Voting power is summed with arbitrary-precision integers while
preserving the documented rounded percentage policy. Revocation removes only
sanctions owned by the revoked case. Watch regressions cover RPC/service
recovery, sequence advancement and evidence expiry beyond height 101.

Additional settlement regressions use the real bank keeper with frozen sender
and recipient accounts. An approved revert (five signed approvals) or escrow
(four-of-five threshold) can move the specified funds without removing either
freeze; ordinary sends and duplicate execution remain rejected. The internal
freeze exemption is scoped to the settlement endpoints and does not bypass
balance checks or other bank restrictions. Escrow execution is tested using an
explicitly stored case: the default policy still has no escrow selection rule.

Both agent signing and transaction signing reject zero and out-of-range
secp256k1 private scalars. Broadcast acknowledgements must contain a matching
JSON-RPC version/request ID, an explicit result code, and the hash of the
submitted transaction. Invalid acknowledgements do not advance the watch
sequence. Legacy `-watch` still needs manual lost-ack reconciliation; `-daemon`
uses the durable confirmation flow above. CheckTx acceptance alone is not proof
of final execution.

Verification on 2026-10-23: the initial runs encountered `no space left on
device`. After clearing about 4.5 GiB of unused standard Go build cache while
preserving the G503 cache, dependencies, keys and logs, both multi-node reruns
passed. Each run also passed the complete Go suite and shell syntax checks.

- `revalidate-five-1`: five validators, five signed revert approvals, four-of-five
  transaction blocking, watch-mode observation and full five-node restart.
  All five nodes retained identical active sanction state after restart, and
  the execution record was unchanged.
- `revalidate-lifecycle-1`: five staking validators and five active agents with
  public keys; stopping node5 triggered jail, and node3 self-unbond deactivated
  the corresponding agents with zero voting power, leaving three active agents.

Both are local experiments with mock risk/LLM services, not production-network
validation. Summaries and metrics are saved under each run ID in the configured
result root. Source code, dependency modules and previous result logs were not
removed during cache cleanup.

The subsequent durable-agent implementation passed the complete `all` regression
run (`regression-1`) and another `five-agent` run (`restart-final`). Both
autonomous runs processed two cases with five votes each, recovered all five
agents from SIGKILL with an unacknowledged execute, and verified two successful
execution events with no duplicate effect and exact balance restoration.
The focused txmanager/chainclient/agent race tests also passed. CI is configured
for the new scenario; these recorded results are local, not a GitHub CI run.

After adding original-transfer receipts and reviewed revocation, the full `all`
run `regression-2` on 2026-10-23 also passed. Two cases approved with five votes
each could not settle the same original twice; execution, a third case opening
and replay after all-node restart were rejected with `ErrAlreadySettled`.
Forged amounts, nonexistent originals, unauthorized review/revoke, authority
signature spoofing and revoke-before-review were rejected. Automatic freeze,
five signed re-evaluations, accepted authority review, revoke and ordinary
transfer resumption succeeded; every node retained the audit record. Existing
single-node, five-validator and lifecycle E2Es, the complete Go suite and focused
race tests passed. These remain local mock risk/LLM/Agent API results.

### Code Quality and Performance Review (2026-10-26)

The focused review covered registry synchronization, case voting, ownership
leases and durable transaction management:

- Registry synchronization reads agents once and indexes operators and IDs,
  removing per-validator full scans while retaining duplicate/collision behavior
  and deterministic state-write order.
- Vote counting uses bounded scans for small committees and an agent-ID index
  for larger inputs. It never tallies by map iteration and retains arbitrary-
  precision weights, first-match duplicate handling and existing approval rules.
- Normal lease maintenance uses two RPC calls instead of three, sharing one
  height-pinned snapshot. It does not cache ownership between calls; the separate
  pre-broadcast validation remains in place.
- Failed-intent reconciliation and network broadcast are separate functions;
  message encoding is shared. Journal-before-send durability, retry ordering and
  returning immediately after requeuing a fenced intent are unchanged.
- WATCH completion now recognizes `watch_only`, distinct from `ok` for other
  actions. Missing records, mismatched cases and inappropriate result codes do
  not satisfy completion.

Local Apple M3 Pro microbenchmarks (rounded medians of three runs):

| Operation | Before | After |
| --- | --- | --- |
| Registry sync, 1000 validators | 441 ms / 301 MB allocated | 6.5 ms / 5.1 MB allocated |
| Vote tally, 1000 voters | 2.23 ms / 8.1 KB allocated | 0.098 ms / 54.7 KB allocated |

Large-set indexing trades allocation bytes for lower CPU time. These are local
operation timings, not network throughput or consensus-latency guarantees.
Rerun with `go test ./x/sanction/keeper -run '^$' -bench 'Benchmark(SyncAgentRegistry|SnapshotTally)' -benchmem`.
Regression tests also compare 500 input sets against the previous tally.

Verification passed the complete Go suite, race tests on five related packages
and the `all` regression run `regression-final`: five-validator/five-agent
automation, recovery, duplicate-settlement rejection, reviewed revocation,
validator lifecycle, ownership failover and a full six-node restart. WATCH
completion has six unit-regression scenarios, not a multi-node WATCH E2E.
These experiments use independent local processes/directories and mock
risk/LLM/Agent API services, not multiple real SSH hosts or production services.
Results are in `regression-final/summary.json` and scenario metrics under the
configured result root.

The 202 protobuf `copylocks` diagnostics and full-history journal scans noted
in that review are addressed by the following addition. Further decomposition
of larger CLI/daemon functions remains a future readability improvement.

### Protobuf Pointers and Journal Indexes

Keeper storage/query, policy, signing and parameter-validation APIs, their
callers and tests now use protobuf pointers. Standard `go vet ./...` passes
with zero diagnostics, without suppressing analyzers. Signing, sign-byte
normalization, decision hashes and independent message copies use `proto.Clone`.
Tests cover nonmutation of inputs/nested slices, concurrent signing of shared
inputs and independent keeper reads. Go callers must rebuild against the new
signatures; this change does not alter proto definitions or stored protobuf
formats.

The bbolt journal now has version `1` `active_intents` / `active_work` indexes:

- Normal intent processing reads only `queued`, `pending` and `failed` rows.
  Failed intents remain eligible for semantic reconciliation and ownership
  fencing, not unconditional automatic retries.
- Work processing reads unfinished items and expired items needing re-evaluation.
- Primary rows and indexes commit in the same write transaction, retaining
  primary-key order. Completed history, signed bytes and audit information are
  not deleted.
- Old journals build indexes once at startup. Missing index buckets of the
  known version are rebuilt; failed migration rolls back and unknown versions
  are rejected. Normal indexed restarts do not scan completed history.
- Old backups still restore after checksum/identity/genesis validation and
  migrate when opened. New backup/restore validation also rejects inconsistent
  index membership. Offline audit/backup may still scan the full journal.

Do not let an old binary write a migrated journal. Take an offline backup before
deployment and use a matching old backup for rollback. Indexing does not repair
arbitrary database corruption, and a growing unresolved failed backlog still
increases reconciliation cost.

On a local Apple M3 Pro, with 1 KiB payloads, 10,000 completed rows and five
pending rows, median list-read time/allocation across three runs changed from
4.6 ms / 16.4 MB to 0.035 ms / 11.3 KB. Small histories may be slower because of
fixed validation overhead; these are not whole-chain performance guarantees.

```bash
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
go test ./dev/agent/internal/txmanager -run '^$' -bench '^BenchmarkJournalActiveIndex$' -benchmem
GOFLAGS='-p=2' bash dev/scripts/ssh-remote-run.sh --experiment all \
  --result-root /tmp/g503-pointer-index --run-id pointer-index
```

CI and SSH runner `checks` / `all` now include standard vet and full race tests.
Regressions cover migration, state transitions, failed-write atomicity, index
corruption, legacy backup restoration and concurrent updates.

The 2026-10-26 `regression-final` (`all`) run passed the complete Go suite, race
tests on all packages, standard vet with zero diagnostics, single-node E2E and
all five-validator/five-agent scenarios. Checks covered lost acknowledgements,
all-agent SIGKILL recovery, duplicate-settlement rejection, reviewed revocation,
expired evidence, failed retries, backup restoration, validator add/jail/unbond,
cross-directory ownership failover with five old processes fenced and a full
six-node restart. Recovery scenarios reported zero duplicate executions or
settlements. Results are under `regression-final/summary.json`,
`logs/go-vet.log`, `logs/go-race.log` and scenario metrics in the result root.
These are local process/mock-service results, not real multi-host SSH,
production-service or successful GitHub CI execution claims.

### Fresh RPCs and Durable History Checkpoints

Resident `-daemon` agents now use a guarded RPC pool for pinned chain reads,
pending-tx polling, ownership leases and signed broadcasts. The operator-client
addition below now extends these checks to signing CLIs and watch polling too.

```json
{
  "rpc_endpoints": ["http://node1:26657", "http://node2:26657", "http://node3:26657"],
  "rpc_safety": {
    "max_block_age_seconds": 15,
    "max_future_skew_seconds": 3,
    "max_height_lag": 2,
    "min_agreement": 2,
    "probe_timeout_seconds": 3
  }
}
```

Merge these fields into the existing agent config. CLI equivalents are
`-rpc-endpoints`, `-rpc-max-block-age`, `-rpc-max-future-skew`,
`-rpc-max-height-lag`, `-rpc-min-agreement` and `-rpc-probe-timeout`.
Without a list, the existing `rpc_url` is guarded as one endpoint. Agreement
defaults to a strict majority of the configured endpoints, not the survivors.

Each tick and broadcast checks chain ID, genesis fingerprint, block age,
future clock skew, `catching_up`, height regression, acceptable height lag and
agreement on one common block hash. Common height/hash and genesis are committed
to `meta/rpc_checkpoint` before a send. Restart and offline backup/restore retain
this checkpoint; a reset/fork with the same genesis is rejected if it changes
the saved block. Slow persistence is followed by another freshness check.
Selection is observable through `rpc_selected` and `rpc_broadcast` JSON events.

A failed broadcast is never immediately replayed on another endpoint. The
existing durable tx manager reconciles signed bytes, sequence and semantic
effects before a retry. No fresh continuous majority means no new sends.
These are trusted-RPC consistency checks, **not cryptographic CometBFT light
client verification**. Keep independent endpoints, synchronized clocks and
retained checkpoint blocks. A malicious/correlated majority, pruning past the
checkpoint or dishonest clocks remain outside this guarantee.

### Five-Host Fault E2E Foundation

The new `g503-multihost` controller provisions five distinct Linux SSH hosts at
one pushed commit. Default invocation only produces a plan and does not use SSH:

```bash
go run ./dev/agent/cmd/g503-multihost \
  -inventory dev/multihost.example.json -revision "$(git rev-parse HEAD)"
```

Real execution requires `-execute` and a new result directory. The inventory
example contains documentation-only IPs and an invalid placeholder SHA; replace
them before execution. See [five-host runbook](dev/README.multihost.md) for
prerequisites, commands, cleanup and limitations. The scenario covers 4:1
partition/heal, 3:2 consensus halt and stale-send suppression, all-five restart,
unique settlement/five votes/balance restoration and common-block convergence.
Only owned PIDs/firewall chains are changed; no global flush or key collection.

Unit tests simulate the controller and exercise the worker with fake firewall
commands. PR CI runs these tests and the no-SSH plan. The existing local
five-validator/five-agent recovery scenario now also stops preferred RPC node1,
requires all five agents to switch to the remaining RPCs and settle a transfer,
then restarts the node. **Five real SSH hosts and actual firewall partition
behavior have not been executed without host details.** Risk/LLM remain mocks.

The verified pre-change checkpoint was pushed as `3243c82`; both GitHub CI jobs
passed in [run 40266240640](https://github.com/yanagihalab/G503/actions/runs/40266240640).
This supersedes the earlier statement that GitHub CI was unconfirmed; it does
not claim that a real five-host experiment passed.

### Controller Recovery and Operator RPC Protection

`g503-ops review-sanction` / `revoke-sanction`, `g503-send`, `g503-validator`,
and one-shot/watch `sanction-agent` now use guarded RPCs with persistent common
block/genesis checkpoints. Watch polling uses the same selected client. Set the
correct chain ID even for read-only watch. Offline dry-runs do not contact RPC.

Operator/send/validator commands accept `-rpc-endpoints`, `-rpc-min-agreement`,
`-rpc-max-block-age`, `-rpc-max-future-skew`, `-rpc-max-height-lag`,
`-rpc-probe-timeout`, `-rpc-genesis-hash`, `-rpc-state-dir`, and `-request-id`.
Agent commands retain their existing RPC policy flags and add `-rpc-state-dir`
and `-request-id` for non-daemon commands.

State defaults to `G503_RPC_STATE_DIR` or `~/.local/state/g503/rpc`. The directory
must be private (`0700`); databases are `0600`, scoped to chain ID and signer,
and exclusively locked. Reused chain IDs in independent experiments need
separate directories. Never delete a checkpoint to accept an unexplained reset.

An explicit request ID saves signed bytes before I/O. Repeating it reconciles
the original hash even if the supplied sequence has changed. Changed messages,
signers, fee, gas or memo are rejected. An unknown lookup or a consumed sequence
without an indexed result stops recovery, not re-signing. When sequence still
matches, only identical bytes may be retransmitted. Without a request ID, CLI
sends are guarded but not durably idempotent. Legacy watch is not the daemon.

```bash
go run ./dev/agent/cmd/g503-ops review-sanction \
  -chain-id g503-lab -rpc-endpoints http://node1:26657,http://node2:26657,http://node3:26657 \
  -rpc-min-agreement 2 -rpc-state-dir /private/state/operator-rpc \
  -key-file /private/authority.key -case-id CASE_ID -reason 'review accepted' \
  -evidence-hash-hex EVIDENCE_SHA256 -account-number NUMBER -sequence SEQUENCE \
  -request-id review-CASE_ID
```

The multihost controller synchronously saves immutable inventory/revision,
owner nonce, pending steps and completed results in locked `controller.db`.
After SIGKILL, use `-execute -resume -results SAME_DIR`: completed phases are
skipped and interrupted operations retain their IDs. Transfers reconcile saved
signed bytes. Worker mutations use `flock`; PID publication precedes exec;
partial initialization never overwrites keys. Interrupted runs retain resources
for resume or `-execute -resume -cleanup`; only verified, cleaned runs pass.
See the [recovery runbook](dev/README.multihost.md).

Compound regressions kill the controller after an accepted real transfer,
resume alongside five agents and require one original broadcast/settlement.
The ownership fixture combines RPC loss, validator add/jail/unbond, restored
journals and old-owner fencing. Reviewed release invokes the real operator CLI,
rejects stale RPC without consuming sequence and succeeds through alternate
RPCs. CI uses local nodes and mock services, not five real SSH hosts.

## Evaluation Metrics

Initial evaluation focuses on:

- delay from anomaly detection to agent consensus;
- success rate of transaction suppression before finalization;
- success rate of approved sanction execution after finalization;
- false positive / false negative behavior of the risk policy;
- robustness when the local LLM or risk service is unavailable.

## Scope

This is a research prototype. It is intended to demonstrate whether validator-
bound AI agents can reach auditable consensus quickly enough to support safer
crypto-asset transactions. Production use would require legal review, validator
governance design, abuse-resistance analysis, secure key management, and careful
policy controls.
