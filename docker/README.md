# Crossref Five-Chain Docker Experiment

This directory starts five isolated `crossrefd` chains and one Hermes relayer.
Hermes opens a full mesh of `crossref` IBC channels between:

- `chain-a <-> chain-b`
- `chain-a <-> chain-c`
- `chain-a <-> chain-d`
- `chain-a <-> chain-e`
- `chain-b <-> chain-c`
- `chain-b <-> chain-d`
- `chain-b <-> chain-e`
- `chain-c <-> chain-d`
- `chain-c <-> chain-e`
- `chain-d <-> chain-e`

The resulting network has 10 pairwise IBC channels and 20 directed
cross-reference paths.

## Prerequisites

- Run commands from the repository root, not from inside `docker/`.
- Build the Linux ARM64 binary before starting Docker.
- Keep Docker Desktop or Docker Engine running.
- If Docker cannot read files under the project directory on macOS, move the
  repository to a directory allowed by Docker file sharing or grant Docker
  access to the parent folder.

## Experiment Flow

At a high level, the experiment does the following:

1. Start five independent single-validator chains.
2. Start one Hermes relayer container.
3. Create a full mesh of `crossref/crossref` IBC channels.
4. Register every domain on every chain.
5. Bind each directed domain pair to its local IBC channel.
6. Submit one checkpoint per source chain.
7. Export source-chain checkpoint proofs as ICS23 proofs.
8. Update destination light clients to the proof heights.
9. Broadcast each checkpoint to the other four chains.
10. Query destination chains to confirm the received cross-references.

## Directed Route Numbers

IBC channel IDs are local to each chain, so several chains can each have their
own `channel-0`. For logs and visual inspection, the experiment assigns one
global directed route number to every path:

| Route | Direction | Actual local endpoint | Actual counterparty endpoint |
| --- | --- | --- | --- |
| `route-00` | `chain-a -> chain-b` | `chain-a/channel-0` | `chain-b/channel-0` |
| `route-01` | `chain-b -> chain-a` | `chain-b/channel-0` | `chain-a/channel-0` |
| `route-02` | `chain-a -> chain-c` | `chain-a/channel-1` | `chain-c/channel-0` |
| `route-03` | `chain-c -> chain-a` | `chain-c/channel-0` | `chain-a/channel-1` |
| `route-04` | `chain-a -> chain-d` | `chain-a/channel-2` | `chain-d/channel-0` |
| `route-05` | `chain-d -> chain-a` | `chain-d/channel-0` | `chain-a/channel-2` |
| `route-06` | `chain-a -> chain-e` | `chain-a/channel-3` | `chain-e/channel-0` |
| `route-07` | `chain-e -> chain-a` | `chain-e/channel-0` | `chain-a/channel-3` |
| `route-08` | `chain-b -> chain-c` | `chain-b/channel-1` | `chain-c/channel-1` |
| `route-09` | `chain-c -> chain-b` | `chain-c/channel-1` | `chain-b/channel-1` |
| `route-10` | `chain-b -> chain-d` | `chain-b/channel-2` | `chain-d/channel-1` |
| `route-11` | `chain-d -> chain-b` | `chain-d/channel-1` | `chain-b/channel-2` |
| `route-12` | `chain-b -> chain-e` | `chain-b/channel-3` | `chain-e/channel-1` |
| `route-13` | `chain-e -> chain-b` | `chain-e/channel-1` | `chain-b/channel-3` |
| `route-14` | `chain-c -> chain-d` | `chain-c/channel-2` | `chain-d/channel-2` |
| `route-15` | `chain-d -> chain-c` | `chain-d/channel-2` | `chain-c/channel-2` |
| `route-16` | `chain-c -> chain-e` | `chain-c/channel-3` | `chain-e/channel-2` |
| `route-17` | `chain-e -> chain-c` | `chain-e/channel-2` | `chain-c/channel-3` |
| `route-18` | `chain-d -> chain-e` | `chain-d/channel-3` | `chain-e/channel-3` |
| `route-19` | `chain-e -> chain-d` | `chain-e/channel-3` | `chain-d/channel-3` |

## Build

Build the Linux ARM64 daemon used by the Docker image:

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o ./build/crossrefd-linux-arm64 ./cmd/crossrefdd
```

For an Intel/AMD Linux Docker host, build `linux/amd64` instead and update the
Dockerfile or output path accordingly.

## Start

Start all five chains and the relayer:

```bash
docker compose -f docker/docker-compose.yml up -d --build
```

The relayer container runs `setup-ibc.sh`, which waits for all chains, imports
Hermes keys, and creates the full mesh of `crossref` channels.

Check container status:

```bash
docker compose -f docker/docker-compose.yml ps
```

Watch relayer logs:

```bash
docker compose -f docker/docker-compose.yml logs -f relayer
```

## Run the Experiment

Run the end-to-end five-chain experiment:

```bash
docker/scripts/run-crossref-experiment.sh
```

The script performs these checks:

1. Prints the `route-00` to `route-19` numbering table.
2. Waits for all 20 directed `crossref` channel endpoints.
3. Registers all five domains on all five chains.
4. Binds every directed domain/channel route.
5. Submits one checkpoint on each source chain.
6. Signs each checkpoint with the source domain's Ed25519 hysteresis key.
7. Queries each source checkpoint with an ICS23 store proof using
   `query crossref checkpoint-proof`.
8. Updates the destination light clients to the source proof heights.
9. Broadcasts each source checkpoint packet to the other four chains.
10. Verifies that every destination chain stores cross-references for the other
   four source chains.

Success ends with:

```text
Five-chain cross-reference experiment passed.
```

## Hysteresis Signature Verification

The Docker experiment now registers an Ed25519 `hysteresis_public_key` for every
domain on every chain. Each submitted checkpoint is signed with the source
domain's deterministic test key, and both `SubmitCheckpoint` and IBC packet
receive verify `hysteresis_signature` against the checkpoint hash. Domains
without a registered public key are still accepted by the module for migration
compatibility, but this five-chain experiment exercises the signature-required
path.

## Query Examples

Query a checkpoint on chain A:

```bash
docker compose -f docker/docker-compose.yml exec -T chain-a \
  crossrefd --home /var/crossref query crossref checkpoint chain-a 1 --output json
```

Query a received cross-reference on chain B for chain A:

```bash
docker compose -f docker/docker-compose.yml exec -T chain-b \
  crossrefd --home /var/crossref query crossref cross-reference chain-b chain-a 1 --output json
```

Export an ICS23 checkpoint proof from chain A:

```bash
docker compose -f docker/docker-compose.yml exec -T chain-a \
  crossrefd --home /var/crossref query crossref checkpoint-proof chain-a 1 --output json
```

## Endpoints

| Chain | RPC | gRPC | REST |
| --- | --- | --- | --- |
| A | `http://localhost:26657` | `localhost:9090` | `http://localhost:1317` |
| B | `http://localhost:26667` | `localhost:9091` | `http://localhost:1318` |
| C | `http://localhost:26677` | `localhost:9092` | `http://localhost:1319` |
| D | `http://localhost:26687` | `localhost:9093` | `http://localhost:1320` |
| E | `http://localhost:26697` | `localhost:9094` | `http://localhost:1321` |

## Reset

Stop containers and remove all chain state:

```bash
docker compose -f docker/docker-compose.yml down -v
```

## Notes

`run-crossref-experiment.sh` assumes the channel and client IDs produced by the
channel creation order in `setup-ibc.sh`. If the full-mesh channel order changes,
update `channel_id()` and `client_for_source()` in the experiment script.

The script leaves the Docker network running after a successful experiment so
that query commands and visual inspection can continue.

## Troubleshooting

- `operation not permitted`: macOS file permissions or Docker file sharing may
  block access. Put the repository in a Docker-shared folder and make sure the
  files are readable by your user.
- `docker: unknown command: docker compose`: install Docker Compose v2 or use
  Docker Desktop, which includes it.
- channels do not appear: inspect relayer logs with
  `docker compose -f docker/docker-compose.yml logs -f relayer`.
- experiment fails after changing channel order: update `channel_id()` and
  `client_for_source()` in `docker/scripts/run-crossref-experiment.sh`.
