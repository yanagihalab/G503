# Development Utilities

This directory contains off-chain development utilities for the sanction PoC.
These tools are intentionally kept outside the on-chain Cosmos SDK module.

## Layout

- `agent/`: local validator-bound sanction agent CLI and clients.
- `mock/`: mock risk and local LLM-compatible services for development.
- `scripts/`: evaluation and demo scripts.

The on-chain implementation should contain only chain code such as proto
definitions, keepers, modules, and app wiring.
