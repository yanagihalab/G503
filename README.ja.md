# G503: Crossrefd Hysteresis Docker Experiment

このリポジトリは、`crossrefd` chain scaffold から切り分けた
hysteresis signature 付き Docker 実験を管理するためのものである。

`.ignite-work/crossrefd` の本体開発と、実験用 README / Docker script の変更を
混ぜないため、別リポジトリとして管理している。

英語版は [README.md](README.md) である。

## このリポジトリの役割

`G503` は Crossref 5 チェーン IBC 実験の companion repository である。含まれるものは次の通りである。

- 署名付き 5 チェーン Docker 実験を説明する README 更新。
- deterministic Ed25519 hysteresis signing を有効化した
  `docker/scripts/run-crossref-experiment.sh`。
- deterministic test signing helper である
  `docker/scripts/hysteresis-sign.go`。
- `crossrefd` checkout に適用できる
  `patches/crossrefd-hysteresis-docker.patch`。

このリポジトリ単体は Cosmos SDK chain checkout ではない。`crossrefd` の Docker
実験関連ファイルに対する overlay として扱う。

## Crossrefd への適用

clean な `crossrefd` checkout で次を実行する。

```bash
git apply /path/to/G503/patches/crossrefd-hysteresis-docker.patch
```

その後、`crossrefd` repository root で通常の 5 チェーン実験を実行する。

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o ./build/crossrefd-linux-arm64 ./cmd/crossrefdd
docker compose -f docker/docker-compose.yml up -d --build
docker/scripts/run-crossref-experiment.sh
```

成功すると最後に次が表示される。

```text
Five-chain cross-reference experiment passed.
```

## 実験の動作

この patch を適用した Docker 実験では次を行う。

1. `chain-a` から `chain-e` までの deterministic Ed25519 hysteresis key を準備する。
2. 全 5 chain 上の全 domain に `hysteresis_public_key` を登録する。
3. source domain key で署名した checkpoint を送信する。
4. 各 source checkpoint の ICS23 proof を取得する。
5. 各 checkpoint を他の 4 chain へ broadcast する。
6. destination chain が期待通り cross-reference を保存したことを検証する。

目的は、local checkpoint submission と IBC packet reception の両方で
signature-required path を通すことである。

## ファイル構成

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

## 注意

- signing helper は deterministic なローカル実験用であり、本番 key 管理には使わない。
- operational key management はこの split repository の範囲外である。
- production chain module の変更は main の `crossrefd` repository 側で管理する。
