# Crossref Five-Chain IBC

Crossref Five-Chain IBC は、Cosmos SDK を使って cross-reference blockchain system を実験するためのプロトタイプである。独自の `x/crossref` module を実装し、それを IBC application module として動作させ、Docker 上で 5 本の `crossrefd` チェーンと 1 つの Hermes relayer を起動できる。

英語版は [README.md](README.md) である。

## このリポジトリに含まれるもの

- `x/crossref`: domain 登録、checkpoint 保存、channel bind、cross-reference packet の送受信、ICS23 proof 検証、hysteresis signature 検証を行う Cosmos SDK module。
- `proto/crossrefd/crossref/v1`: message、query、genesis、packet、module type の protobuf 定義。
- `app`: `crossrefd` chain への module wiring。
- `docker`: 5 本の独立した `crossrefd` chain と 1 つの Hermes relayer。
- `docker/scripts/run-crossref-experiment.sh`: 5 チェーン full mesh の channel 作成確認、checkpoint 登録、ICS23 proof 取得、packet broadcast、cross-reference 保存確認まで行う end-to-end 実験スクリプト。

## アーキテクチャ

このプロトタイプでは次の 5 domain を扱う。

- `chain-a`
- `chain-b`
- `chain-c`
- `chain-d`
- `chain-e`

Hermes は全ペアに対して `crossref/crossref` の IBC channel を開く。これにより、10 本の双方向 channel connection と、20 方向の directed cross-reference route ができる。各 source chain は自身の checkpoint を登録し、それを他の 4 チェーンへ broadcast できる。

受信側は次を検証する。

1. packet の source domain が登録済み source chain と一致していること。
2. packet が許可された local channel から届いていること。
3. checkpoint hash が checkpoint 内容と整合していること。
4. 既知の前回 checkpoint がある場合、previous checkpoint hash が一致していること。
5. source checkpoint が source chain に実在することを、IBC light client で検証される ICS23 store proof により確認できること。
6. source domain が hysteresis public key を登録している場合、hysteresis signature が正しいこと。

## Crossref Module

現在の module は次を実装している。

- `RegisterDomain`: domain ID、chain ID、validator set hash、metadata URI、任意の Ed25519 hysteresis public key を登録する。
- `BindDomainChannel`: local domain と remote domain のペアを IBC port/channel に bind する。
- `SubmitCheckpoint`: hash と任意の hysteresis signature を検証して local checkpoint を保存する。
- `SendCrossReferencePacket`: bind 済み remote domain へ checkpoint を 1 件送信する。
- `BroadcastCrossReferencePacket`: 1 つの source checkpoint を bind 済みの全 remote domain へ送信する。
- `ReceiveCrossReferencePacket`: checkpoint packet を受信し、ICS23 source-store proof を含めて検証する。
- domain、channel、checkpoint、cross-reference、checkpoint proof export 用の query endpoint。

## Hysteresis Signature 検証

domain は Ed25519 の `hysteresis_public_key` を登録できる。

この key が登録されている domain では、local checkpoint submission と IBC packet receive の両方で `hysteresis_signature` が必須となる。署名は checkpoint hash に対して検証される。checkpoint hash は現在の block/app state と previous checkpoint hash に commit するため、checkpoint chain の改ざん検知性を持つ。

`hysteresis_public_key` が未登録の domain は、ローカル実験と移行互換性のため従来通り受け入れる。

5 チェーン Docker 実験では全 domain にこの key を登録し、署名付き checkpoint を送信するため、end-to-end の IBC 経路で署名検証が実際に実行される。

## 必要なもの

- macOS または Linux shell
- 生成済み Cosmos SDK app を build できる Go toolchain
- Docker Desktop または Docker Engine + Docker Compose
- Docker image pull や Go module cache が空のときの依存解決用ネットワーク

## クイックスタート

リポジトリ root で実行する。

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o ./build/crossrefd-linux-arm64 ./cmd/crossrefdd
docker compose -f docker/docker-compose.yml up -d --build
docker/scripts/run-crossref-experiment.sh
```

成功すると最後に次が表示される。

```text
Five-chain cross-reference experiment passed.
```

Docker 実験の詳細は [docker/README.ja.md](docker/README.ja.md) を参照する。

## Visualizer

crossref の test と 5 チェーン broadcast route map を確認するための browser visualizer を同梱している。

```bash
open visualizer/crossref-test-visualizer.html
```

visualizer では次を確認できる。

- keeper / IBC の behavior test
- 1 つの relayer に集約された network topology
- `route-00` から `route-19` までの directed packet flow
- source-chain checkpoint proof 検証
- Hermes relayer を経由する packet の animation

repository root から smoke test を実行できる。

```bash
node visualizer/verify-visualizer.mjs
```

## よく使うコマンド

Go test を実行する:

```bash
go test -count=1 ./...
```

protobuf codegen を再実行する:

```bash
make proto-gen
```

5 チェーン Docker network を起動する:

```bash
docker compose -f docker/docker-compose.yml up -d --build
```

full experiment を実行する:

```bash
docker/scripts/run-crossref-experiment.sh
```

停止して chain state を削除する:

```bash
docker compose -f docker/docker-compose.yml down -v
```

## ローカルエンドポイント

| Chain | RPC | gRPC | REST |
| --- | --- | --- | --- |
| A | `http://localhost:26657` | `localhost:9090` | `http://localhost:1317` |
| B | `http://localhost:26667` | `localhost:9091` | `http://localhost:1318` |
| C | `http://localhost:26677` | `localhost:9092` | `http://localhost:1319` |
| D | `http://localhost:26687` | `localhost:9093` | `http://localhost:1320` |
| E | `http://localhost:26697` | `localhost:9094` | `http://localhost:1321` |

## 現在のプロトタイプ範囲

このリポジトリは実験実装である。cross-reference design、IBC application の動作、proof validation、multi-chain routing をローカルで検証することを目的としている。実運用に向けては、key management、chain upgrade policy、詳細な threat model、relayer operation、論文定義の `H(S_n-1)` を checkpoint hash から独立した形式で扱う厳密な hysteresis signature format などの追加検討が必要である。
