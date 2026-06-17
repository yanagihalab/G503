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

## Off-Chain Development Utilities

The `dev/` directory is intentionally off-chain:

- `dev/agent`: local AI-agent CLI and clients.
- `dev/mock/risk-service`: mock Chainalysis-compatible risk service.
- `dev/mock/llm-service`: mock local LLM explanation service.
- `dev/scripts/evaluate-sanction-latency.sh`: latency evaluation helper.

The current AI-agent development mode assumes a centrally managed local LLM.
Future work can move toward distributed validator-operated LLM agents.

## Test

```bash
go test ./...
```

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
