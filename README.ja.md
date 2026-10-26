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

## Cosmos SDK Scaffold

`app/` と `cmd/g503d/` は、crossref とは独立した G503 用の軽量 Cosmos SDK scaffold である。
現段階では `x/sanction` の store、Msg/Query router、genesis、`PrepareProposal` /
`ProcessProposal` hook を `baseapp.BaseApp` に配線している。ローカルの署名済み tx demo に
必要な最小 module として、`auth`、`bank`、`staking`、`consensus`、
`slashing` も統合している。

```bash
go run ./cmd/g503d version
```

この scaffold は研究 PoC であり、本番用 chain template ではない。agent vote や risk report
の真正性は、まず `x/sanction` 内の agent 署名検証で扱う。実運用には、validator 運用、
鍵管理、policy governance、濫用耐性の強化が必要である。

## ローカル demo マイルストーン

ローカル demo は、次の一連の流れを再現できるように整備している。

1. ローカルチェーンを起動する。
2. 通常送金 tx を流す。
3. agent が pending tx を検知して risk screening と local LLM 判定を実行する。
4. agent が sanction tx を送信する。
5. 検知から sanction 実行までの一連の demo を再現できるようにする。

現在は local E2E demo、5 validator sanction flow、validator lifecycle 追従確認用の
script を含む。

## Off-Chain Development Utilities

`dev/` は chain 本体ではなく、開発補助用である。

- `dev/agent`: local AI-agent CLI と各種 client。
- `dev/mock/agent-api`: 機械的な policy 評価を行う deterministic mock AI-agent API。
- `dev/mock/risk-service`: Chainalysis 互換を想定した mock risk service。
- `dev/mock/llm-service`: local LLM 説明生成の mock service。
- `dev/scripts/evaluate-sanction-latency.sh`: latency 評価 helper。
- `dev/scripts/evaluate-agent-api.sh`: mock agent API 評価 helper。

現段階では AI エージェントは中央集権的に管理する local LLM を利用する想定である。
将来的には各 validator が独自に LLM agent を運用する分散型構成へ拡張できる。

agent は risk/LLM 判定後、生成した `MsgSubmitRiskReport`、`MsgOpenSanctionCase`、
`MsgSubmitSanctionVote` を Cosmos SDK tx にまとめて CometBFT RPC へ送信できる。

```bash
go run ./dev/agent/cmd/sanction-agent \
  -tx ./sample-tx.json \
  -agent-id validator-1 \
  -privkey-hex <32-byte-secp256k1-private-key-hex> \
  -broadcast \
  -rpc-url http://127.0.0.1:26657
```

pending tx の監視では `/unconfirmed_txs` から raw tx を取得し、Cosmos SDK tx として decode した
すべての `cosmos.bank.v1beta1.MsgSend` と各 denom を risk screening に流す。
複数送金では最も強い措置を必要とする判定を選び、同じ tx に対して一度だけ送信する。
RPC / risk / LLM / Agent API の一時エラーは stderr に記録して再試行する。
重複検知の記憶は現在の mempool に限定する。

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

watch は署名時に現在の block height を取得し、証跡の有効期間を更新する。
`broadcast_tx_sync` が受理したら次の sequence に進む。watch の再起動時には account number /
sequence をチェーンから確認して指定する。同じ鍵を他プロセスと共有した場合の衝突解決や、
応答が失われた送信の自動照合は未対応である。Ctrl-C / SIGTERM で終了でき、出力は JSON Lines。

### 永続・自律 Agent

`-daemon` では提案・各 agent の独立評価と投票・制裁実行・tx の確定確認までを自動で行う。
登録済みの5 agent それぞれを、別の鍵・config・永続ディレクトリで常駐させる。

```bash
go run ./dev/agent/cmd/sanction-agent \
  -config ./agent1.json -daemon -state-dir ./.g503/agent1 \
  -target-type address -poll-interval 500ms
```

既存 JSON config の chain ID、RPC、risk / LLM URL、agent ID、mode `0600` の `key_file`
を利用する。`"daemon":{"enabled":true,"state_dir":"./.g503/agent1","start_height":0}`
でも指定できる。残り4 agent もそれぞれ別 config と state directory で起動する。
account number / sequence は自動取得するため手動指定は不要である。

- 同期書き込みする bbolt の `agent.db`（mode `0600`）に検知情報・評価証跡・block cursor・
  送信 intent・署名済み tx を保存してから送信する。同じ DB の二重起動は排他ロックで拒否する。
- active registry と on-chain policy を読み、各 agent が独立に評価・投票する。
  共通 case ID は `auto-<大文字TX_HASH>`。agent 順位による待機で提案・実行の競合を減らし、
  先頭 agent が停止した場合は他 agent が処理を引き継げる。
- mempool と成功した確定済み block を追跡する。初回は現在の tip より後を対象とし、
  新規 DB のみ `-start-height` で過去を明示的に走査できる。再起動時は保存済み cursor を再開する。
- 再送には backoff を設け、RPC 障害を「tx 不在」と扱わない。indexed tx のバイト列・hash・
  DeliverTx 結果、同じ確定 height の account / case / vote / execution を照合する。
- address 対象の case は、元の成功した `bank.MsgSend` と sender / recipient / amount /
  denom が一致することを検証してから投票する。返還は補償送金であり、確定済み履歴の巻き戻しではない。

停止した agent の journal は、チェーン接続なしで確認できる。

```bash
go run ./dev/agent/cmd/sanction-agent -daemon-status -state-dir ./.g503/agent1
```

intent は `queued` / `pending` / `confirmed` / `satisfied`（on-chain で効果を確認済み）/
`cancelled` / `failed` を記録する。CheckTx 受理だけで確定済みにはしない。
確定した実行失敗や gas / fee エラーは無限再送せず、`failed` として運用者による確認を要求する。

永続領域は保持し、1署名アカウントにつき1 journal を所有させる。daemon の別ディレクトリ・
別ホスト間の送信は、次節の on-chain lease と世代番号で排他制御する。
信頼できる RPC、tx indexing、必要な block / result の保持、同じチェーンの継続を前提とする。
期限切れ証跡は再評価し、failed intent の明示的な再試行とバックアップ復元は次節の手順で扱う。
バックアップなしのDB消失、チェーン初期化、非対応の恒久エラーは運用者対応が必要である。
全員署名の revert は case 作成時に固定した全投票者の承認を待ち、引き継ぎやjailで署名要件を緩和しない。
pending の `-target-type tx` 抑制は best effort であり、既に確定した tx は取り消せない。
この E2E は明示的な address 対象の補償送金を検証する。従来の `-watch` は非永続の処理経路であり、
現在は下記のRPC保護を適用している。

```bash
bash dev/scripts/run-five-agent-e2e.sh
```

5 validator と5 agent を起動し、中央の mock risk / LLM / Agent API を共有する。
試験用 loopback proxy が実行 tx の受理応答を保留した瞬間に全 agent を SIGKILL し、
同じ journal で再起動した後、2件目も無操作で処理する。各 case の5署名、元 tx あたり case 1件、
成功した実行イベント計2件、送受信者の残高復元、中断した実行 tx の再送0回を検査する。
`logs/autonomous-metrics.json` と停止後 journal snapshot を保存する。
SSH runner の `--experiment five-agent` と PR CI にも組み込んでいる。

### 障害復旧

未送信の証跡が期限切れになった場合は risk / LLM / Agent API を呼び直し、現在の height と
policy で再署名する。単に古い証跡の expiry を延長する処理ではない。TTL は CLI の
`expiry-height - height` とチェーンの `evidence_ttl_blocks` の小さい方を使い、最低3ブロックを要求する。
通常は十分な投票時間を確保できる既定の100ブロックを使う。

投票開始後に証跡または投票期間が切れた address case は、`auto-<HASH>-r1`、`-r2` の
新しい case で合意を取り直す。`previous_case_id` によって旧 case を参照し、チェーン側で
同じ元送金、新しい observed height、旧 case の期限切れ、後継の重複がないことを検証する。
旧 case は `EXPIRED` として証跡・投票を保持し、新 case には投票をコピーしない。
実行時も期限切れの証跡を拒否する。実行済み・否決済み・解除済みの case は再開せず、
既に有効な凍結を自動解除することもない。pending tx の抑制期限を延長する機能ではない。

gas / fee / 残高などを修復した後、対象の failed intent を明示的に再試行できる。

```bash
go run ./dev/agent/cmd/sanction-agent -config agent1.json -daemon -target-type address \
  -retry-intent 'open/auto-<HASH>' -retry-reason 'gas corrected after operator review' \
  -retry-request-id gas-repair-001
```

再試行前に過去の全 tx hash、確定結果、同じ height の account sequence と case / vote /
execution を照合する。既に効果があれば再送せず確定・達成済みとする。RPC障害や未確定の
送信を「失敗」と見なして別txを作らない。再試行は out-of-gas、残高・fee不足、mempool満杯、
sequence不一致、証跡期限切れに限定し、無権限・署名不正・不正リクエストなどは拒否する。
open / vote は再評価・再署名し、execute は現在の評価と承認済み措置が一致することも確認する。
元の署名済みbytes、attempt、旧メッセージ、理由・height・request IDを保持する。
同じ request ID の再起動は同じ復旧要求として扱い、再失敗しても無限に再試行しない。
request ID省略時は理由のSHA-256を使う。別の修復を行う場合は新しいIDを指定する。
未送信の期限切れ証跡と `open` の証跡期限エラーだけは、再評価後に自動復旧する。

journal は chain ID だけでなく genesis fingerprint に紐付け、同じIDでもgenesisが異なる
チェーンへの再送を拒否する。同一genesisからの履歴リセット・別forkの判定は保証せず、
同じ確定履歴の継続が前提である。信頼できるRPCの fingerprint は鍵を読まず確認できる。

```bash
go run ./dev/agent/cmd/sanction-agent -config agent1.json -print-genesis-hash
```

既存の fingerprint 未登録journal は、安全側に起動を拒否する。元のネットワークのgenesisを
確認できた場合だけ、`-journal-genesis-hash <TRUSTED_GENESIS_HASH>` で初回の登録を明示する。
初期化後の別チェーンを元のチェーンと思い込んで登録してはいけない。

バックアップ・復元は agent を停止して行う。稼働中DBのロック、既存アーカイブ・復元先の
上書き、identity / genesis不一致、チェックサム不一致、壊れたDBを拒否する。

```bash
go run ./dev/agent/cmd/sanction-agent -state-dir ./.g503/agent1 \
  -journal-backup ./backups/agent1-001.tar
# 出力の sha256 と manifest.genesis_hash を、信頼できる別の場所にも保管する。
# state-dir は新規の復元先。元のagentは停止し、同じ鍵で同時起動しない。
go run ./dev/agent/cmd/sanction-agent -config agent1.json -target-type address \
  -state-dir ./.g503/agent1-restored -journal-restore ./backups/agent1-001.tar \
  -backup-sha256 <TRUSTED_ARCHIVE_SHA256> -journal-genesis-hash <TRUSTED_GENESIS_HASH>
go run ./dev/agent/cmd/sanction-agent -config agent1.json -daemon -target-type address \
  -state-dir ./.g503/agent1-restored
```

アーカイブはmode `0600`、上限128MiB。DB snapshotとmanifestを単一tarに保存し、
fsyncと上書きしない公開処理を行う。復元時は内部digest・DB構造・送信hashも検査する。
checksumは改変検出用であり、暗号化や署名済みバックアップではない。信頼できるchecksumの
別保管が必要で、証跡や再送可能な署名済みtxを含むためアクセスを制限する。
鍵ファイルは対象外なので別途安全に保管する。復元後はチェーンを照合してから送信を再開する。
オンラインバックアップ、自動的な破損DB修復は未対応である。

```bash
bash dev/scripts/run-agent-recovery-e2e.sh
```

5 validator / 5 agentで、実行txの応答消失中のbackup/restore、全agentのgas不足、
期限切れ証跡の再評価と明示的再試行、成功前の古いbackup復元後の再送抑止、
投票中断・期限切れ後の新しい5署名を検証する。イベント数、sequence、残高も照合し、
`logs/recovery-metrics.json` に保存する。mock risk / LLM / Agent APIを使うローカル試験である。
PR CIとSSH runnerの `--experiment agent-recovery` / `all` に組み込んでいる。

2026-10-23 の全回帰 `regression-1` と追加復旧試験 `final-1` / `final-2` は成功した。
最新試験では復旧対象2件が実行され、二重実行0件、残高復元、古いfailed backup復元後の
sequence不変を確認した。RPC接続先が不通でAPI tokenもない状態でのオフライン復元も確認した。
全Goテストと関連raceテストも成功している。CI設定は更新済みだが、これらはローカルの結果である。

### 署名所有権と固定投票者

`-daemon` は起動ごとに新しいランダムな session ID を生成し、署名アカウント単位の
`MsgClaimAgentLease` を確定させてから送信する。別ホストでも同じチェーン上のleaseを参照するので、
共有ファイルシステムは不要である。生存中の所有者があれば既定では起動を拒否する。
`-lease-ttl-blocks` は6〜100、既定24ブロック。半分の期間が残った時点で更新する。
取得・更新も署名付きtxであり、gas・fee・sequenceを消費する。

障害後の計画的な引き継ぎでは、旧agentを停止し、復元先で期限切れ待ちを明示する。

```bash
go run ./dev/agent/cmd/sanction-agent -config agent1.json -daemon -target-type address \
  -state-dir ./.g503/agent1-restored -lease-ttl-blocks 24 -lease-wait 90s
```

lease取得のたびに世代番号epochが増え、署名対象のtx memoにepochとsession IDを含める。
agentは毎tickと送信直前に所有権を照合する。期限切れ・所有者変更を検知した旧プロセスは
再取得せず終了する。更新・照合の失敗中も送信しない。チェーンはCheckTxとDeliverTxの両方で照合し、
旧世代やleaseなしのtxを拒否する。lease登録後は、そのアカウントによる通常送金や手動CLIにも
有効な所有権memoが必要であり、`-watch`や従来CLIへ切り替えて排他を迂回できない。
期限切れleaseを削除して世代をリセットする操作は提供しない。
`GET /sanction/v1/leases/{signer_address}` で現在の所有者・epoch・期限heightを確認できる。
復元journalの過去txは先に確定状態を照合し、未達成かつ旧世代で実行不能のものだけ再署名する。
確定済みtxや他agentが完了した処理を再実行しない。

これは協調するagentプロセスの排他制御であり、漏洩した秘密鍵の悪意ある利用を防ぐKMS/HSMではない。
信頼できる同一チェーンが前提で、チェーン停止中はheight期限が進まず引き継げない。
バックアップは引き続き対象journalを閉じて行う。sessionの所有権をbackupから復活させない。

caseは作成heightの有効agentをID順にsnapshotし、ID・signer・公開鍵・投票権・quorum・
revert全員承認要否を固定する。投票資格・署名検証・集計・実行判定はこのsnapshotを使う。
追加validatorは過去caseへ投票できず、jail・unbond・投票権変更・params変更でも過去caseの
必要承認は減らない。既存snapshotメンバーはjail後も過去caseの資格を保持するが、
新規caseの投票者にはならない。署名が揃わなければ待機し、証跡期限切れ後の新caseで
最新registryによる合意を取り直す。旧投票は新caseへコピーしない。
snapshotはquery・genesis export/import・全ノード再起動後も保持する。
snapshotのない旧caseを現在のregistryで暗黙補完しない。既存チェーンの更新には、
過去の投票者を検証できる明示的なupgrade/migrationが必要で、バイナリの差し替えだけでは適用しない。

```bash
bash dev/scripts/run-ownership-snapshot-e2e.sh
# SSH runnerにも --experiment ownership-snapshot を追加。allとPR CIに含む。
```

5 validator・5 agentから開始し、稼働中の別journal起動拒否と停止時backupを確認する。
5台目agentをSIGSTOPし、残る4 agentが送金を検知・独立評価して4票を送る。その後全旧agentを
SIGSTOPし、6台目validator追加、node5のjail、node3のunbond、別stateディレクトリへの復元を行う。
旧caseは5名・新caseは最新の4名を保持し、4署名での返還と新validatorによる過去case投票を拒否する。
復元agentが5票目と実行を完了し、旧プロセス再開後の追加送信停止・旧ホスト再起動拒否、旧memo・memo欠落計10件の拒否、
全6ノード再起動後のsnapshot・精算台帳を照合する。`logs/ownership-metrics.json`を保存する。
これは独立ディレクトリとプロセスを別ホスト相当として使うローカル試験であり、実SSH複数ホストや
本番ネットワーク分断の試験ではない。risk / LLM / Agent APIもmockである。

複数段階の構成変更中に意図せずcaseが失効しないよう、この試験だけは管理者署名で証跡TTLを
300ブロック・投票期間を100ブロックに設定する。通常の既定値は変更しない。

2026-10-26 の `regression-final`（`all`）は成功した。自動5票、復帰後の旧5プロセスの
追加送信0件、旧ホストの再起動拒否、古い/欠落memoの拒否10件、実行イベント1件、二重精算0件、
残高復元を確認した。既存の単一ノード・5 validator・誤検知解除・障害復旧・validator lifecycle、
全Goテストと関連race検査も成功した。CI設定は更新済みだが、これらはローカルの検証結果である。

### Mock AI Agent API

`dev/mock/agent-api` は、AI agent decision endpoint の deterministic な代替実装である。
LLM のように自由記述で判断するのではなく、擬似評価として再現可能な機械判定を行う。
raw risk score に対して、risk category の重大度、direct / indirect exposure、送金額、
trusted / blocked address、証跡不足を加減点し、最終的な `calibrated_score` を計算する。

response には次を含める。

- `raw_risk_score` と `calibrated_score`;
- `action` と `vote_option`;
- `review_level`;
- `evidence_status`;
- `findings`;
- `required_evidence`;
- deterministic な `decision_id`。

API を起動する。

```bash
go run ./dev/mock/agent-api \
  -listen 127.0.0.1:8083 \
  -api-token local-agent-token
```

機械的評価 suite を実行する。

```bash
AGENT_API_TOKEN=local-agent-token \
  bash dev/scripts/evaluate-agent-api.sh
```

`sanction-agent` は、この API の結果を使って on-chain message を作れる。`-agent-api-url`
を指定した場合、API が返す `calibrated_score` を `RiskReport.risk_score` として提出し、
元の risk score / response ID、評価済み risk、LLM explanation、agent decision JSON から
`EvidenceHash` / `LlmRationaleHash` を作る。署名には `-key-file` または `-privkey-hex` が必要で、
チェーンへの送信には `-broadcast` と正しい account number / sequence を指定する。
API の agent ID・証跡・投票・action の整合性を検査し、local policy と矛盾する場合は署名を止める。
`tx` 対象は確定済みでも `block_tx` までである。既存仕様で返金処理を検証する場合は
`address` 対象を使用する。これは元のブロック履歴を削除する処理ではなく、残高の逆送金である。

```bash
go run ./dev/agent/cmd/sanction-agent \
  -tx ./sample-tx.json \
  -agent-id validator-1 \
  -agent-api-url http://127.0.0.1:8083 \
  -agent-api-token "$G503_AGENT_API_TOKEN"
```

統合経路は次で確認できる。

```bash
bash dev/scripts/evaluate-agent-flow.sh
```

### SSH Server Experiment

実際の SSH サーバ上で実験するための同期・セットアップ・実行 helper を用意している。
ローカルから `ssh` と `rsync` で repository をサーバへ転送し、サーバ側で依存関係を確認して
実験を実行し、最後にログをローカルへ回収する。

サーバ側の前提は Linux、Go `1.25.10` 以上（`go.mod` に追従）、`bash`、`curl`、`git`、
`jq`、`perl`、`xxd`、`rsync` である。
Ubuntu / Debian 系であれば `--install-deps` により Go 以外の一般的な package を `apt-get`
で導入できる。

```bash
bash dev/scripts/ssh-server-experiment.sh \
  --host <ssh-host> \
  --user <ssh-user> \
  --identity ~/.ssh/id_ed25519 \
  --experiment agent-flow
```

指定できる `--experiment` は次の通りである。

- `checks`: shell 構文確認、全 Go テスト、標準 `go vet ./...`、全 race テスト。
- `agent-flow`: mock risk / mock LLM / mock agent API と署名付き report / vote の生成確認。
- `local-e2e`: risk service 障害復旧、watch の2件連続自動送信、Agent API の署名付き tx、
  on-chain report hash 照合、成立済み sanction による tx 除外。
- `five-validator`: 5 validator sanction E2E。
- `five-agent`: 5 agent 自律合意形成・永続送信・応答消失・SIGKILL 再起動 E2E。
- `sanction-safety`: 上記に加え、元送金検証・二重返還拒否・再審査後の権限付き解除を検証。
- `agent-recovery`: 上記に加え、期限切れ証跡・failed再試行・journal復元による障害復旧を検証。
- `validator-lifecycle`: 5 validator の追加 / 公開鍵登録 / jail / unbond と agent registry 追従 E2E。
- `all`: 上記の主要実験を順番に実行。

結果はサーバ側の `g503-ssh-lab/ssh-results/<run-id>` に保存され、ローカルの
`.g503/ssh-results/<host>-<run-id>` にも回収される。
失敗時も `summary.json` に失敗した step と exit code を記録し、ログ回収を試みる。
全ノード再起動試験は実行バイナリの PID を停止し、5ノードすべての RPC が停止したことと、
再起動後に全ノードから同じ sanction state を取得できることを検査する。

SSH 接続なしで runner 自体の回帰テストを行える。

```bash
bash dev/scripts/test-ssh-helpers.sh
```

### Operator Tools

`dev/agent/cmd/g503-ops` は運用者向け helper を提供する。

- `config-template`: `sanction-agent` 用 JSON config を出力する。
- `rotate-key`: 新しい mode `0600` の鍵を生成する。既存ファイルを上書きしない。
- `audit-bundle`: 証跡本体を base64 で内包し、同じ証跡から同じ SHA-256 を作る。
- `review-sanction`: 証跡ハッシュ付きの誤検知再審査を authority の署名で記録する。
- `revoke-sanction`: 署名付き `MsgRevokeSanction` の生成・送信補助。

鍵生成は既存 agent の on-chain 鍵更新までは行わない。新しい `g503d init` は agent と別の
制裁管理アカウントを生成し、`app_state.sanction.authority` に設定する。研究用の秘密鍵は
mode `0600` の `config/demo-keys.json` の `authority` に保存する。本番の鍵保管方式ではない。
この authority は sanction module に限定され、SDK の他 module の `gov` authority は変更しない。
未指定の既存 genesis は従来の `gov` address にフォールバックする。`x/gov` 自体は未統合である。

### 元送金検証と誤検知解除

成功した tx 全体の実行後に、`bank.MsgSend` の各メッセージ・各 denom の送金内容を
post-handler でチェーンに保存する。CheckTx、simulation、途中のメッセージが失敗した tx は
記録しない。返還・escrow は前ブロック以前の記録と tx hash / `message_index` / sender /
recipient / amount / denom が一致することを、case 作成時と実行時に検証する。
`message_index` は tx 内の0始まりのメッセージ位置で、省略時は0である。

精算キーは `tx hash + message_index + denom` であり、case ID や action を変更しても
同じ送金のコインを再精算できない。返還と escrow も同じ台帳を共有する。送金失敗時には
精算済みにせず、制裁解除後も精算記録を削除しない。返還は履歴の巻き戻しではない。
この記録方式の導入前の送金には自動で証明を補完せず、安全側に拒否する。
稼働済みチェーンへの適用には明示的な upgrade / genesis 移行が必要である。

解除は、管理者が証跡を確認して `MsgReviewSanction` を署名・確定させ、その後
`MsgRevokeSanction` を署名・確定させる2段階とする。再審査の理由と32-byte証跡ハッシュ、
管理者、再審査 height、解除 height、解除理由を保持する。再審査だけでは凍結は解除しない。
受理した再審査は case ごとに1件で、上書きできない。誤検知と認定する管理者の判断を
記録する仕組みであり、AI が証跡の真偽を保証するものではない。

```bash
go run ./dev/agent/cmd/g503-ops audit-bundle \
  -case case.json -votes votes.json -decision review-evidence.json -out review-bundle.json
go run ./dev/agent/cmd/g503-ops review-sanction \
  -rpc-url http://127.0.0.1:26657 -chain-id g503-local-1 \
  -key-file authority.key -case-id <CASE_ID> -reason 'false positive confirmed' \
  -evidence-hash-hex <BUNDLE_SHA256> -account-number <NUMBER> -sequence <SEQUENCE>
# 再審査 tx の確定を確認し、更新後の sequence で解除を送る。
go run ./dev/agent/cmd/g503-ops revoke-sanction \
  -rpc-url http://127.0.0.1:26657 -chain-id g503-local-1 \
  -key-file authority.key -case-id <CASE_ID> -reason 'review accepted' \
  -account-number <NUMBER> -sequence <NEXT_SEQUENCE>
```

新しい query は `/sanction/v1/authority`、`/sanction/v1/tx/{hash}/transfers`、
`/sanction/v1/tx/{hash}/settlements`、`/sanction/v1/cases/{id}/review` である。
記録は genesis export/import とノード再起動で保持する。公開の異議申立て受付、否認された
再審査の管理、損失補償の裁定、multisig / governance による管理権限の運用は今後の課題である。

```bash
bash dev/scripts/run-sanction-safety-e2e.sh
```

既存の5 agent強制終了・復旧試験に続き、異なる2 case を返還前に各5署名で承認して
2件目の実行拒否を検証する。さらに5 agent を常駐させて自動凍結し、無権限解除の拒否、
再審査なしの解除拒否、証跡付き再審査、正規解除、通常送金再開を検査する。
再審査では訂正後の mock risk と中央 Agent API で5件の再評価を行い、各 agent が署名した
「制裁不要」の評価を証跡に含め、管理者が受理する。解除権限を5署名の再投票に置き換えるものではない。
全5ノード再起動後も精算台帳と監査記録を照合し、二重返還を再度拒否する。
結果は `logs/safety-metrics.json`、各試験ログ、再審査証跡に保存する。
PR CI と SSH runner の `--experiment sanction-safety` / `all` に組み込んでいる。

Agent API は bearer token、role ヘッダの許可リスト、任意の mTLS、IP 単位のレート制限、
body サイズ制限、HTTP timeout を持つ。role ヘッダは認証主体に紐付く本番 RBAC ではない。
`-client-ca-file` を指定する場合は server TLS 証明書と鍵も必須である。

`dev/agent/cmd/g503-dashboard` は read-only の小さな local dashboard であり、現時点では
sanction params と agent registry state を REST API から表示する。

## Test

```bash
go test -count=1 ./...
```

case 作成では report と tx hash・受領者・送金内容の一致を検証する。
無効な vote option、負・ゼロの金額、空の validator set による全員承認を拒否する。
投票権の合計は多倍長整数で計算し、既存の切り上げパーセント判定を維持する。
古い case の解除では、その case が所有する sanction / freeze のみ削除する。
これらの異常系、watch の通信復旧・sequence 更新・101 height 超での証跡更新を回帰テストに含める。

追加の回帰テストでは実 bank keeper を使い、送金元・返還先の両方が凍結されていても、
5 agent 全員が署名した revert、または 4/5 の承認を得た escrow が実行できることを確認する。
凍結は解除せず、通常送金・二重実行は引き続き拒否する。内部の凍結例外は制裁送金の
送金元・送金先だけに限定し、残高検証やその他の bank 制約は維持する。
escrow は保存済み case の実行テストであり、デフォルト policy に escrow 選択ルールはない。

agent 署名・tx 署名の両方で、ゼロ・曲線位数以上の secp256k1 秘密鍵を拒否する。
broadcast 応答は JSON-RPC version / request ID、明示的な結果コード、送信 tx hash の一致を
検証し、不正な応答では watch の sequence を進めない。従来の `-watch` の応答消失時は
運用者による照合が必要だが、`-daemon` は上記の永続確定確認を行う。
CheckTx 受理だけで最終実行成功を保証するものではない。

2026-10-23 の初回検証は `no space left on device` で中断した。その後、今回使用していない
標準 Go ビルドキャッシュ約 4.5 GiB を整理し、多ノード・validator lifecycle の両方の再検証が
成功した。各実行の前段にある全 Go テストと shell 構文チェックも成功した。

- `revalidate-five-1`: 5 validator、5 agent の revert 署名、4/5 承認による送金抑制、
  watch 監視、全5ノードの停止・再起動を確認。再起動後の active sanction は全ノードで一致し、
  実行記録も再起動前と一致した。
- `revalidate-lifecycle-1`: 5 staking validator と公開鍵付き有効 agent 5件の登録を確認。
  node5 の停止による jail、node3 の自己委任 unbond に追従し、対象 agent の無効化・投票権0、
  最終的な有効 agent 3件を確認した。

いずれも mock risk / LLM を使用するローカル実験であり、本番ネットワーク条件の検証ではない。
summary と metrics は指定した result root 内の各 run ID 配下に保存する。
キャッシュ整理では G503 用キャッシュ、ソース、依存モジュール、鍵、既存結果ログは保持した。

その後の永続 agent 実装では、`all` の全回帰実行（`regression-1`）と、追加の
`five-agent` 実行（`restart-final`）が成功した。両自律試験とも2件を各5署名で自動処理し、
実行 tx 応答が未受領の状態で全 agent を SIGKILL してから復旧した。
成功した実行イベント計2件・二重実行0件・残高の完全復元を確認した。
txmanager / chainclient / agent の race 検出付きテストも成功した。
CI に新試験を追加しているが、ここに記載した成功結果はローカル実行であり GitHub CI の実績ではない。

元送金台帳と再審査付き解除の追加後、2026-10-23 の `regression-2`（`all`）も成功した。
異なる2 case を各5署名で承認し、2件目の実行・追加 case 作成・全ノード再起動後の再実行を
いずれも `ErrAlreadySettled` で拒否した。改変金額・存在しない元送金、無権限の再審査と解除、
管理者署名のなりすまし、再審査前の解除も拒否した。自動凍結から5件の署名付き再評価、
管理者の再審査受理、解除、通常送金再開まで成功し、全5ノードで監査記録を確認した。
既存の単一ノード・5 validator・validator lifecycle と Go 全テスト、関連 race テストも成功した。
ローカルの mock risk / LLM / Agent API を使った結果であり、本番サービスの評価ではない。

### ソース品質と性能の確認（2026-10-26）

直近の registry 同期、case 投票集計、ownership lease、永続送信管理を中心に確認・改善した。

- registry 同期は agent 一覧を1回だけ読み、operator / agent ID を索引化する。
  validator ごとの全件再走査を除去し、重複 operator・ID 衝突時の既存動作と state 更新順序を保つ。
- 投票集計は少数票では短い走査、多数票では agent ID 索引を用いる。
  map の列挙順には依存せず、多倍長整数・投票重複時の先勝ち・既存の承認条件を維持する。
- lease 維持の通常経路は同じ height の status と lease を用い、RPC を3回から2回に削減した。
  呼び出し間の ownership キャッシュは行わず、送信直前の再検証も維持する。
- txmanager は failed intent の照合と network broadcast を別関数へ分離し、message encoding を共通化した。
  送信前の journal 永続化、再送順序、fenced intent の再キュー後に同一 Step で送信しない条件は変えていない。
- WATCH 実行記録の `watch_only` を正常完了として照合するよう修正した。
  返還などの `ok` と区別し、記録欠落・別 case・不適切な結果コードは完了としない。

Apple M3 Pro のローカル microbenchmark（各3回の中央値、端数丸め）:

| 対象 | 変更前 | 変更後 |
| --- | --- | --- |
| 1000 validator の registry 同期 | 約441 ms / 301 MB割当 | 約6.5 ms / 5.1 MB割当 |
| 1000 voter の集計 | 約2.23 ms / 8.1 KB割当 | 約0.098 ms / 54.7 KB割当 |

多数票の索引化はメモリ割当量と引き換えに CPU 時間を短縮する。これは単体処理の実測であり、
ネットワークの throughput・合意遅延を保証する値ではない。benchmark は
`go test ./x/sanction/keeper -run '^$' -bench 'Benchmark(SyncAgentRegistry|SnapshotTally)' -benchmem`
で再実行できる。500組の入力で旧集計との一致を比較する回帰テストも含む。

検証では全 Go テスト、関連5 package の race 検出付きテスト、`all` 回帰実行
（`regression-final`）が成功した。5 validator / 5 agent の自動処理、障害復旧、
二重返還拒否、再審査付き解除、validator lifecycle、所有権切替、6ノード全再起動を確認した。
WATCH 完了照合は6種類の単体回帰テストで確認しており、多ノード WATCH E2E ではない。
実験はローカルの独立 process / directory と mock risk / LLM / Agent API を用いたもので、
複数の実 SSH host や本番サービスの評価ではない。結果は result root 内の
`regression-final/summary.json` と各 scenario の metrics に保存する。

上記の確認時に残っていた protobuf `copylocks` 警告202件と journal 全履歴走査は、
次の追加実装で対応した。大きな CLI / daemon 関数のさらなる整理は引き続き改善対象である。

### protobuf API のポインタ化と journal 索引

keeper の保存・取得、policy、署名、params validation と関連する呼び出し元・テストを
protobuf ポインタへ統一した。標準 `go vet ./...` は警告0件で成功し、analyzer の抑制は行っていない。
署名、署名対象の正規化、decision hash、case / report の独立コピーには `proto.Clone` を使う。
入力や入れ子の byte slice を変更しないこと、共有入力の並行署名、keeper 読み出しの独立性をテストする。
Go API の引数・戻り値は変わるため呼び出し元の再ビルドが必要だが、この変更では
proto 定義や保存される protobuf 形式は変更していない。

journal には bbolt の `active_intents` / `active_work` 索引と version `1` を追加した。

- `queued` / `pending` / `failed` intent だけを通常処理へ渡す。failed は他 agent の成功照合や
  ownership fencing のため対象に残し、自動で無条件再送するわけではない。
- work は未完了、または `CASE_STATUS_EXPIRED` の再評価対象だけを取得する。
- 本体と索引を同じ write transaction で永続化し、主キー順の処理順序を維持する。
  完了履歴・署名済み tx・監査情報は削除しない。
- 旧 journal は起動時に一度だけ索引を構築する。既知 version の索引 bucket 欠落も再構築する。
  移行失敗時は rollback し、未知 version は拒否する。version が揃った再起動では全履歴を走査しない。
- 旧形式の backup は既存の checksum / identity / genesis 検証後に復元でき、起動時に移行する。
  新形式の backup / restore は索引と本体の不一致も拒否する。offline の監査・backup 時は全件検証を行う。

移行後の journal を旧バイナリで更新しないこと。運用前に停止状態で backup を取得し、
旧バイナリへ戻す場合は対応する旧 backup を使う。索引化は任意の DB 破損を修復する機能ではなく、
未解決の failed intent 自体が増えれば照合コストも増える。

Apple M3 Pro、各行1 KiBの payload、完了履歴1万件・pending 5件、3回の中央値では、
journal 一覧読込が約4.6 ms / 16.4 MB割当から約0.035 ms / 11.3 KB割当へ減った。
小さい履歴では索引照合の固定コストが上回る場合があり、chain 全体の性能保証ではない。

```bash
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
go test ./dev/agent/internal/txmanager -run '^$' -bench '^BenchmarkJournalActiveIndex$' -benchmem
GOFLAGS='-p=2' bash dev/scripts/ssh-remote-run.sh --experiment all \
  --result-root /tmp/g503-pointer-index --run-id pointer-index
```

CI と SSH runner の `checks` / `all` に標準 vet・全 race テストを組み込んだ。
移行、状態遷移、更新失敗の原子性、索引破損、旧 backup 復元、並行更新の回帰テストを含む。

2026-10-26 の `regression-final`（`all`）では、全 Go テスト、全 package の race テスト、
標準 vet（警告0件）、単一ノード、5 validator / 5 agent の全 E2E が成功した。
応答消失・全 agent の強制終了からの復旧、二重返還拒否、再審査付き解除、
期限切れ証跡・failed 再試行・backup 復元、validator 追加 / jail / unbond、
別 directory への所有権切替と旧5 process の送信抑止、6ノード全再起動を確認した。
各復旧試験で二重実行・二重返還は0件だった。結果は指定 result root の
`regression-final/summary.json`、`logs/go-vet.log`、`logs/go-race.log` と各 scenario の metrics に保存する。
これはローカル process / mock service による検証であり、実 SSH 複数 host や本番サービス、
GitHub CI の実行成功を示すものではない。

### RPC 鮮度判定と複数 RPC 切替

常駐 `-daemon` の chain query、pending tx 監視、lease 更新、signed tx 送信を
共通の RPC pool に統合した。下記の運用CLI統合により、当初は対象外だった
単発CLIとwatch監視にも保護を適用した。

既存 agent JSON config に以下を追加する。リスト省略時も `rpc_url` 1台を検証する。

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

CLI は `-rpc-endpoints`、`-rpc-max-block-age`、`-rpc-max-future-skew`、
`-rpc-max-height-lag`、`-rpc-min-agreement`、`-rpc-probe-timeout` に対応する。
一致数の既定値は設定済み endpoint の過半数で、障害時に自動で引き下げない。

- chain ID / genesis、block 時刻、未来時刻の許容差、同期中状態、高さ後退、高さの遅れを確認する。
- 共通 height の block hash が必要数一致した場合のみ、その履歴に固定して読み取る。
- 共通 height / hash / genesis を journal の `meta/rpc_checkpoint` に保存してから送信する。
  再起動・backup / restore 後も保持し、同一 genesis の履歴差替えを拒否する。
- 保存中に鮮度期限を超えた場合も送信しない。RPC 選択・送信は JSON event に記録する。
- broadcast 応答消失時に別 RPC へ即再送せず、既存の永続送信管理で確定・sequence・効果を照合する。

これは信頼する RPC 群の整合性チェックであり、暗号学的な light client 検証ではない。
独立した接続先・時刻同期・checkpoint block の保持が必要で、悪意ある過半数や
checkpoint より先まで prune された RPC に対する可用性は保証しない。

### 5 ホスト障害 E2E の実行基盤

新しい `g503-multihost` は、push 済みの同一 commit で Linux 5台を初期化し、
5 validator / 5 agent を起動する。既定は plan 表示のみで SSH 接続しない。

```bash
go run ./dev/agent/cmd/g503-multihost \
  -inventory dev/multihost.example.json -revision "$(git rev-parse HEAD)"
```

実行には `-execute` と未使用の `-results` を明示する。設定例の IP / SHA はダミーのため、
実ホスト情報に置き換える。[5ホスト手順書](dev/README.multihost.md) に前提・実行・復旧をまとめた。
4:1 分断と再接続、3:2 停止時の送信抑止、全5ノード再起動、重複返還なし・5票・残高復元、
共通 block hash への収束を検査する。実験専用の所有者マーカー / PID / firewall chain のみを
操作し、全体 flush や秘密鍵の回収は行わない。

controller 模擬試験・worker の firewall 代替試験・SSH を使わない plan を CI に組み込んだ。
既存のローカル5 validator / 5 agent E2E には、優先 RPC の node1 停止中に全5 agent が
残り RPC へ切替え、制裁完了後に node1 を復帰させる試験も追加した。
**接続情報未提供のため、実SSH 5台・実firewall分断は未実行である。** risk / LLM も mock を用いる。

今回まず検証済み状態を `3243c82` として push し、
[GitHub CI run 40266240640](https://github.com/yanagihalab/G503/actions/runs/40266240640)
の両 job 成功を確認した。以前の「GitHub CI 未確認」はこの checkpoint について解消済みであり、
実5ホストの検証成功を意味しない。

### Controller 復旧と運用CLIのRPC保護

`g503-ops` の再審査・解除、`g503-send`、`g503-validator`、単発／watchの
`sanction-agent` に共通RPC保護を適用した。watchのpending取得も同じ選択済みRPCを使う。
読み取り専用watchでも正しい `-chain-id` を明示する。offline dry-runでは接続しない。

運用・送金・validator CLIは `-rpc-endpoints`、`-rpc-min-agreement`、鮮度・時刻差・
height差・probe timeoutの各flag、`-rpc-genesis-hash`、`-rpc-state-dir`、`-request-id`
を受け付ける。agentは既存RPC設定に加えて単発／watch用の `-rpc-state-dir` と
単発操作用の `-request-id` を使える。

既定保存先は `G503_RPC_STATE_DIR`、未指定時は `~/.local/state/g503/rpc`。
directoryは `0700`、DBは `0600` を要求し、chain IDと署名者ごとに排他管理する。
再起動後もgenesisと履歴を照合する。同じchain IDの別実験には別directoryを使い、
不明な履歴変更を受け入れるためにcheckpointを削除しない。

要求IDを指定すると、署名済みbytesを送信前に保存する。同じIDの再実行では元のhashを照合し、
入力sequenceが更新されていても新しいtxに署名し直さない。message・署名者・fee・gas・memoの
変更は拒否する。照合失敗や、txが見つからないままsequenceが消費済みの場合は停止する。
sequenceが同じ場合も再送できるのは同一bytesのみ。ID未指定のCLIには永続的な重複抑止はなく、
旧watchは永続daemonの代替ではない。

controllerは `controller.db` にinventory／commit・所有者・未完了step・完了結果を同期保存する。
同時起動を排他ロックで拒否し、SIGKILL後は `-execute -resume -results 同じdirectory` で再開する。
完了済みstepを飛ばし、送金は同じ要求IDで照合する。workerにはflockとexec前のPID永続化を追加し、
部分初期化された鍵を上書きしない。中断時は再開用の資源を保持するため、終了させる場合は
`-execute -resume -cleanup` を明示する。[復旧手順](dev/README.multihost.md)を参照。

複合E2Eでは、送金受理直後のcontroller強制終了→再開、5 agentの合意形成、
元送金のbroadcast 1回・返還1回を検査する。別の試験ではRPC停止にvalidator追加／
jail／unbond、5 journal復元、旧process復帰の送信抑止を組み合わせる。
再審査・解除は実際の `g503-ops` を呼び、古いRPCでは送信・sequence消費せず、
正常RPCへの切替で完了することを検査する。CIはローカルノードとmockサービスを使い、
実SSHホストの成功を意味しない。

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
