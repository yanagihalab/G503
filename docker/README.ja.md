# Crossref 5チェーン Docker 実験

このディレクトリでは、5本の独立した `crossrefd` チェーンと、1つの Hermes relayer を起動する。
Hermes は次の組み合わせで `crossref` IBC channel の full mesh を開く。

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

結果として、10本の双方向 IBC channel ペアと、20方向の cross-reference 経路ができる。

## 前提条件

- コマンドは `docker/` の中ではなく、リポジトリ root から実行する。
- Docker 起動前に Linux ARM64 版 binary を build する。
- Docker Desktop または Docker Engine を起動しておく。
- macOS で Docker が project directory を読めない場合は、Docker file sharing で許可された場所に repository を置くか、親 folder への access を許可する。

## 実験の流れ

この実験は大まかに次を行う。

1. 5本の独立した single-validator chain を起動する。
2. 1つの Hermes relayer container を起動する。
3. `crossref/crossref` IBC channel の full mesh を作成する。
4. すべての chain に全 domain を登録する。
5. 各 directed domain pair を local IBC channel に bind する。
6. 各 source chain で checkpoint を1つ登録する。
7. source-chain checkpoint proof を ICS23 proof として export する。
8. destination light client を proof height まで更新する。
9. 各 checkpoint を他の4チェーンへ broadcast する。
10. destination chain を query し、受信済み cross-reference を確認する。

## 経路の通し番号

IBC の channel ID はチェーンごとのローカルIDである。そのため、複数のチェーンがそれぞれ `channel-0` を持つ。
ログや visualizer で追いやすくするため、この実験では20方向すべての経路に `route-00` から `route-19` までの通し番号を割り当てている。

| Route | 方向 | 実ローカル endpoint | 実 counterparty endpoint |
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

## ビルド

Docker image 内で使う Linux ARM64 版の daemon をビルドする。

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o ./build/crossrefd-linux-arm64 ./cmd/crossrefdd
```

Intel/AMD Linux Docker host で動かす場合は `linux/amd64` で build し、Dockerfile または出力 path を合わせて変更する。

## 起動

5本のチェーンと relayer を起動する。

```bash
docker compose -f docker/docker-compose.yml up -d --build
```

relayer コンテナは `setup-ibc.sh` を実行する。
このスクリプトは全チェーンの起動を待ち、Hermes key を読み込み、`crossref` channel の full mesh を作成する。

container の状態を確認する。

```bash
docker compose -f docker/docker-compose.yml ps
```

relayer log を確認する。

```bash
docker compose -f docker/docker-compose.yml logs -f relayer
```

## 実験の実行

5チェーンの end-to-end 実験を実行する。

```bash
docker/scripts/run-crossref-experiment.sh
```

このスクリプトは次を確認する。

1. `route-00` から `route-19` までの通し番号表を表示する。
2. 20方向すべての `crossref` channel endpoint が存在するまで待つ。
3. 5つの domain を全5チェーンに登録する。
4. すべての directed domain/channel route を bind する。
5. 各 source chain に checkpoint を1つ登録する。
6. 各 checkpoint に source domain の Ed25519 hysteresis key で署名する。
7. `query crossref checkpoint-proof` で各 source checkpoint の ICS23 store proof を取得する。
8. destination 側 light client を source proof height まで更新する。
9. 各 source checkpoint packet を他の4チェーンへ broadcast する。
10. 各 destination chain が、他の4つの source chain の cross-reference を保存していることを確認する。

成功時は最後に次のメッセージが表示される。

```text
Five-chain cross-reference experiment passed.
```

## Hysteresis Signature 検証

Docker 実験では、全 chain 上の全 domain に Ed25519 の `hysteresis_public_key` を登録する。各 checkpoint は source domain の deterministic test key で署名され、`SubmitCheckpoint` と IBC packet 受信の両方で `hysteresis_signature` が checkpoint hash に対する署名として検証される。module 自体は移行互換性のため公開鍵未登録の domain も受け入れるが、この 5 チェーン実験では署名必須の経路を通す。

## Query 例

chain A の checkpoint を確認する。

```bash
docker compose -f docker/docker-compose.yml exec -T chain-a \
  crossrefd --home /var/crossref query crossref checkpoint chain-a 1 --output json
```

chain B に保存された chain A の cross-reference を確認する。

```bash
docker compose -f docker/docker-compose.yml exec -T chain-b \
  crossrefd --home /var/crossref query crossref cross-reference chain-b chain-a 1 --output json
```

chain A から ICS23 checkpoint proof を export する。

```bash
docker compose -f docker/docker-compose.yml exec -T chain-a \
  crossrefd --home /var/crossref query crossref checkpoint-proof chain-a 1 --output json
```

## エンドポイント

| Chain | RPC | gRPC | REST |
| --- | --- | --- | --- |
| A | `http://localhost:26657` | `localhost:9090` | `http://localhost:1317` |
| B | `http://localhost:26667` | `localhost:9091` | `http://localhost:1318` |
| C | `http://localhost:26677` | `localhost:9092` | `http://localhost:1319` |
| D | `http://localhost:26687` | `localhost:9093` | `http://localhost:1320` |
| E | `http://localhost:26697` | `localhost:9094` | `http://localhost:1321` |

## リセット

コンテナを停止し、全チェーンの状態を削除する。

```bash
docker compose -f docker/docker-compose.yml down -v
```

## 注意点

`run-crossref-experiment.sh` は、`setup-ibc.sh` の channel 作成順序によって生成される channel ID と client ID を前提としている。
full mesh の channel 作成順序を変更した場合は、実験スクリプト内の `channel_id()` と `client_for_source()` も更新する。

実験スクリプトは、成功後も Docker network を起動したままにする。
これにより、追加の query 実行や visualizer による確認を続けられる。

## トラブルシュート

- `operation not permitted`: macOS の file permission または Docker file sharing が access を妨げている可能性がある。repository を Docker が共有できる folder に置き、現在の user が読める状態にする。
- `docker: unknown command: docker compose`: Docker Compose v2 を install するか、Compose v2 同梱の Docker Desktop を使う。
- channel が作成されない: `docker compose -f docker/docker-compose.yml logs -f relayer` で relayer log を確認する。
- channel 作成順序を変えた後に experiment が失敗する: `docker/scripts/run-crossref-experiment.sh` の `channel_id()` と `client_for_source()` を更新する。
