# G503: Crossrefd Hysteresis Docker Experiment

This repository tracks the hysteresis-signature Docker experiment that was split
out from the `crossrefd` chain scaffold.

It is intentionally managed separately from the main `.ignite-work/crossrefd`
workspace so that experiment-specific README and Docker script changes do not
mix with core chain development.

Japanese documentation is available in [README.ja.md](README.ja.md).

## What This Repository Is

`G503` is a small companion repository for the Crossref five-chain IBC
experiment. It stores:

- README updates that explain the signed five-chain Docker experiment.
- `docker/scripts/run-crossref-experiment.sh` with deterministic Ed25519
  hysteresis signing enabled.
- `docker/scripts/hysteresis-sign.go`, a deterministic test signing helper.
- `patches/crossrefd-hysteresis-docker.patch`, the patch that can be applied to
  a `crossrefd` checkout.

This repository is not a standalone Cosmos SDK chain checkout. It is an overlay
for the relevant Docker experiment files.

## Apply To Crossrefd

From a clean `crossrefd` checkout:

```bash
git apply /path/to/G503/patches/crossrefd-hysteresis-docker.patch
```

Then run the normal five-chain experiment from the `crossrefd` repository root:

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o ./build/crossrefd-linux-arm64 ./cmd/crossrefdd
docker compose -f docker/docker-compose.yml up -d --build
docker/scripts/run-crossref-experiment.sh
```

Success ends with:

```text
Five-chain cross-reference experiment passed.
```

## Experiment Behavior

The patched Docker experiment:

1. Prepares deterministic Ed25519 hysteresis keys for `chain-a` through
   `chain-e`.
2. Registers all five domains on all five chains with their
   `hysteresis_public_key`.
3. Submits checkpoints signed by the source domain key.
4. Collects ICS23 proofs for each source checkpoint.
5. Broadcasts each checkpoint to the other four chains.
6. Verifies that the destination chains store the expected cross-references.

The goal is to exercise the signature-required path in both local checkpoint
submission and IBC packet reception.

## Files

```text
README.md
README.ja.md
docker/
  README.md
  README.ja.md
  scripts/
    run-crossref-experiment.sh
    hysteresis-sign.go
patches/
  crossrefd-hysteresis-docker.patch
```

## Notes

- The signing helper is deterministic and intended for local experiments only.
- Operational key management is out of scope for this split repository.
- Keep production chain module changes in the main `crossrefd` repository.
