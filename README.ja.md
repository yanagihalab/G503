# G503: Cosmos SDK AI Sanction Chain

G503 は、暗号資産の高リスク送金を抑制するための Cosmos SDK プロトタイプである。
対象は独立した sanction chain 開発であり、次の 3 つだけを扱う。

1. Chainalysis の address screening などを想定した異常検知。
2. バリデータに紐付いた AI エージェントによる DAO 的な合意形成。
3. ブロックチェーン上での transaction 承認抑制、または承認後の制裁実行。

英語版は [README.md](README.md) である。

## リポジトリ構成

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

## 基本アイデア

詐欺、制裁対象、資金洗浄などに関わる可能性があるアドレスへの送金を検知した場合、
利用者、ノード管理者、またはバリデータに紐付いた AI エージェントが risk report を
提出する。その後、AI エージェントが watch / block / freeze / escrow / revert などの
制裁措置について投票し、合意形成を行う。

finalize 前であれば validator は proposal 処理から対象 transaction を除外する。
承認後に制裁実行が必要な場合は、合意済みの agent decision に基づく特別な transaction
で freeze、escrow、revert などを実行する。

## On-Chain Module

`x/sanction` module は次を提供する。

- agent 登録;
- risk report 提出;
- sanction case 作成;
- AI-agent vote 提出;
- sanction 実行と revoke;
- `PrepareProposal` / `ProcessProposal` で使う active transaction sanction;
- query endpoint と genesis import/export。

proto 定義は `proto/sanction/v1` に置く。

## Off-Chain Development Utilities

`dev/` は chain 本体ではなく、開発補助用である。

- `dev/agent`: local AI-agent CLI と各種 client。
- `dev/mock/risk-service`: Chainalysis 互換を想定した mock risk service。
- `dev/mock/llm-service`: local LLM 説明生成の mock service。
- `dev/scripts/evaluate-sanction-latency.sh`: latency 評価 helper。

現段階では AI エージェントは中央集権的に管理する local LLM を利用する想定である。
将来的には各 validator が独自に LLM agent を運用する分散型構成へ拡張できる。

## Test

```bash
go test ./...
```

## 評価指標

初期評価では次を見る。

- 異常検知から agent consensus までの遅延時間。
- finalize 前の transaction suppression 成功率。
- approve 後の sanction execution 成功率。
- risk policy の false positive / false negative。
- local LLM や risk service 障害時の堅牢性。

## Scope

このリポジトリは研究プロトタイプである。実運用には、法的整理、validator governance、
濫用耐性、secure key management、policy control の精査が必要である。
