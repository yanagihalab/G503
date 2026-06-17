# AI agent based transaction sanction system specification

## 1. Scope

本仕様書は、Cosmos SDK appchain に追加する AI エージェント型トランザクション制裁機構の実装仕様を定める。対象は初期 PoC であり、中央集権的に管理されたローカル LLM、Chainalysis 互換のリスク照会、バリデータ紐付きエージェント署名、`x/sanction` module、未確定 tx の抑制、確定済み tx の補償制裁を含む。

本仕様は Cosmos SDK 上の独立した新規 module として `x/sanction` を追加する前提で記述する。

## 2. Terminology

| Term | Meaning |
| --- | --- |
| Risk report | 外部リスク照会と LLM 説明をまとめた署名付き報告 |
| Sanction case | 1 つの tx または address に対する制裁審議単位 |
| Sanction vote | バリデータ紐付きエージェントによる署名付き投票 |
| Active sanction | 未確定 tx 抑制または address 凍結として有効な制裁状態 |
| Nullify | ブロック履歴を消すのではなく、独自 token state 上で補償的に効果を取り消す処理 |
| Central LLM | 初期 PoC で全エージェントが共有するローカル LLM サーバ |
| Policy version | リスクスコアから制裁判断を導く deterministic rule のバージョン |

## 3. Directory Layout

```text
proto/sanction/v1/
  genesis.proto
  query.proto
  tx.proto
  types.proto

x/sanction/
  module.go
  autocli.go
  keeper/
    keeper.go
    keys.go
    msg_server.go
    query_server.go
    genesis.go
    policy.go
    signature.go
    execution.go
  types/
    codec.go
    errors.go
    genesis.go
    keys.go
    policy.go

dev/
  agent/
    cmd/sanction-agent/
  internal/riskclient/
  internal/llmclient/
  internal/policy/
  internal/signer/
  internal/watcher/

dev/
  mock/
    risk-service/
    llm-service/

dev/
  scripts/
    evaluate-sanction-latency.sh
```

## 4. Proto Types

### 4.1 AgentInfo

```proto
message AgentInfo {
  string agent_id = 1;
  string validator_operator_address = 2;
  string signer_address = 3;
  bytes public_key = 4;
  uint64 voting_power = 5;
  bool active = 6;
  string metadata_uri = 7;
}
```

### 4.2 RiskReport

```proto
message RiskReport {
  string report_id = 1;
  bytes tx_hash = 2;
  string sender = 3;
  string recipient = 4;
  string denom = 5;
  string amount = 6;
  string source = 7;
  string source_response_id = 8;
  uint32 risk_score = 9;
  repeated string risk_categories = 10;
  string exposure_type = 11;
  uint64 observed_height = 12;
  uint64 expiry_height = 13;
  string policy_version = 14;
  bytes evidence_hash = 15;
  bytes llm_rationale_hash = 16;
  string submitter = 17;
  bytes submitter_signature = 18;
}
```

`evidence_hash` は外部リスク照会レスポンス、tx metadata、LLM 入力を canonical JSON にしたものの hash とする。LLM の自然言語出力全文は原則 off-chain storage に保存し、オンチェーンには `llm_rationale_hash` を置く。

### 4.3 SanctionCase

```proto
message SanctionCase {
  string case_id = 1;
  bytes tx_hash = 2;
  string target_address = 3;
  SanctionTargetType target_type = 4;
  SanctionAction requested_action = 5;
  CaseStatus status = 6;
  repeated string report_ids = 7;
  uint64 opened_height = 8;
  uint64 voting_deadline_height = 9;
  uint64 executed_height = 10;
  string policy_version = 11;
  bytes decision_hash = 12;
}

enum SanctionTargetType {
  SANCTION_TARGET_TYPE_UNSPECIFIED = 0;
  SANCTION_TARGET_TYPE_TX = 1;
  SANCTION_TARGET_TYPE_ADDRESS = 2;
}

enum SanctionAction {
  SANCTION_ACTION_UNSPECIFIED = 0;
  SANCTION_ACTION_WATCH = 1;
  SANCTION_ACTION_BLOCK_TX = 2;
  SANCTION_ACTION_FREEZE_ADDRESS = 3;
  SANCTION_ACTION_ESCROW_FUNDS = 4;
  SANCTION_ACTION_REVERT_TRANSFER = 5;
}

enum CaseStatus {
  CASE_STATUS_UNSPECIFIED = 0;
  CASE_STATUS_PENDING = 1;
  CASE_STATUS_APPROVED = 2;
  CASE_STATUS_REJECTED = 3;
  CASE_STATUS_EXECUTED = 4;
  CASE_STATUS_EXPIRED = 5;
  CASE_STATUS_REVOKED = 6;
}
```

### 4.4 SanctionVote

```proto
message SanctionVote {
  string case_id = 1;
  string agent_id = 2;
  VoteOption option = 3;
  SanctionAction approved_action = 4;
  string reason_code = 5;
  bytes rationale_hash = 6;
  uint64 signed_height = 7;
  bytes signature = 8;
}

enum VoteOption {
  VOTE_OPTION_UNSPECIFIED = 0;
  VOTE_OPTION_APPROVE = 1;
  VOTE_OPTION_REJECT = 2;
  VOTE_OPTION_ABSTAIN = 3;
}
```

署名対象は次の canonical bytes とする。

```text
chain_id || module_name || case_id || agent_id || option ||
approved_action || reason_code || rationale_hash || signed_height
```

### 4.5 ExecutionRecord

```proto
message ExecutionRecord {
  string case_id = 1;
  SanctionAction action = 2;
  bytes tx_hash = 3;
  string target_address = 4;
  string executor = 5;
  uint64 executed_height = 6;
  string result_code = 7;
  string result_message = 8;
  bytes state_change_hash = 9;
}
```

### 4.6 Params

```proto
message Params {
  uint32 watch_threshold = 1;
  uint32 block_threshold = 2;
  uint32 freeze_threshold = 3;
  uint32 revert_threshold = 4;
  uint32 quorum_threshold = 5;
  bool unanimous_required_for_revert = 6;
  uint64 evidence_ttl_blocks = 7;
  uint64 voting_period_blocks = 8;
  repeated string accepted_risk_sources = 9;
  repeated string high_risk_categories = 10;
  repeated SanctionAction allowed_actions = 11;
}
```

## 5. Store Keys

```text
Agent/{agent_id} -> AgentInfo
AgentBySigner/{signer_address} -> agent_id
RiskReport/{report_id} -> RiskReport
RiskReportByTx/{tx_hash}/{report_id} -> true
RiskReportByAddress/{address}/{report_id} -> true
SanctionCase/{case_id} -> SanctionCase
CaseByTx/{tx_hash}/{case_id} -> true
CaseByAddress/{address}/{case_id} -> true
SanctionVote/{case_id}/{agent_id} -> SanctionVote
ActiveTxSanction/{tx_hash} -> case_id
FrozenAddress/{address} -> case_id
ExecutionRecord/{case_id} -> ExecutionRecord
Params -> Params
```

## 6. Msg Service

### 6.1 MsgRegisterAgent

Authority-only message. genesis または governance により、バリデータとエージェント署名鍵を登録する。

```proto
rpc RegisterAgent(MsgRegisterAgent) returns (MsgRegisterAgentResponse);
```

Validation:

- `authority` must match module authority.
- `agent_id` must be unique.
- `signer_address` must be unique.
- `voting_power` must be positive.

### 6.2 MsgSubmitRiskReport

エージェントまたは監視ノードがリスク報告を提出する。

```proto
rpc SubmitRiskReport(MsgSubmitRiskReport) returns (MsgSubmitRiskReportResponse);
```

Validation:

- `source` is in `accepted_risk_sources`.
- `risk_score` is 0 to 100.
- `expiry_height` is greater than current height.
- `evidence_hash` is non-empty.
- `submitter_signature` verifies against registered agent key when submitter is an agent.

Effects:

- Store `RiskReport`.
- Create indexes by tx hash and recipient address.
- Emit `EventRiskReportSubmitted`.

### 6.3 MsgOpenSanctionCase

リスク報告をもとに制裁ケースを開始する。

```proto
rpc OpenSanctionCase(MsgOpenSanctionCase) returns (MsgOpenSanctionCaseResponse);
```

Validation:

- All referenced reports exist and are not expired.
- Deterministic policy recommends requested action.
- Duplicate active case for same tx and action is rejected.

Effects:

- Store `SanctionCase` with `PENDING` status.
- Set voting deadline.
- Emit `EventSanctionCaseOpened`.

### 6.4 MsgSubmitSanctionVote

登録済みエージェントが署名付き投票を提出する。

```proto
rpc SubmitSanctionVote(MsgSubmitSanctionVote) returns (MsgSubmitSanctionVoteResponse);
```

Validation:

- Case exists and status is `PENDING`.
- Agent exists and is active.
- No duplicate vote by same agent for same case.
- Signature is valid.
- Approved action is allowed by params.

Effects:

- Store `SanctionVote`.
- Recompute quorum deterministically.
- If approve quorum is reached, set case `APPROVED`.
- If reject quorum is reached, set case `REJECTED`.
- If approved action is `BLOCK_TX`, write `ActiveTxSanction`.

### 6.5 MsgExecuteSanction

承認済みケースを実行する。

```proto
rpc ExecuteSanction(MsgExecuteSanction) returns (MsgExecuteSanctionResponse);
```

Validation:

- Case status is `APPROVED`.
- Case has not been executed.
- Quorum requirement is still satisfied.
- For `REVERT_TRANSFER`, all active agents must have approved if `unanimous_required_for_revert` is true.

Effects by action:

| Action | Effect |
| --- | --- |
| `BLOCK_TX` | Ensure `ActiveTxSanction` exists |
| `FREEZE_ADDRESS` | Write `FrozenAddress` |
| `ESCROW_FUNDS` | Move available funds to module account if token module supports it |
| `REVERT_TRANSFER` | Apply compensating transfer if original transfer is reversible within this chain |

### 6.6 MsgRevokeSanction

誤検知や異議申し立てにより制裁を解除する。

```proto
rpc RevokeSanction(MsgRevokeSanction) returns (MsgRevokeSanctionResponse);
```

Validation:

- Authority-only or approved by governance.
- Case exists.

Effects:

- Set case status `REVOKED`.
- Delete active tx sanction or frozen address if applicable.
- Store revocation event.

### 6.7 MsgUpdateParams

Authority-only message. policy threshold、quorum、accepted sources を更新する。

## 7. Query Service

```proto
rpc Agent(QueryAgentRequest) returns (QueryAgentResponse);
rpc Agents(QueryAgentsRequest) returns (QueryAgentsResponse);
rpc RiskReport(QueryRiskReportRequest) returns (QueryRiskReportResponse);
rpc RiskReportsByTx(QueryRiskReportsByTxRequest) returns (QueryRiskReportsByTxResponse);
rpc SanctionCase(QuerySanctionCaseRequest) returns (QuerySanctionCaseResponse);
rpc SanctionCasesByTx(QuerySanctionCasesByTxRequest) returns (QuerySanctionCasesByTxResponse);
rpc SanctionVotes(QuerySanctionVotesRequest) returns (QuerySanctionVotesResponse);
rpc ActiveTxSanction(QueryActiveTxSanctionRequest) returns (QueryActiveTxSanctionResponse);
rpc FrozenAddress(QueryFrozenAddressRequest) returns (QueryFrozenAddressResponse);
rpc ExecutionRecord(QueryExecutionRecordRequest) returns (QueryExecutionRecordResponse);
rpc Params(QueryParamsRequest) returns (QueryParamsResponse);
```

## 8. Deterministic Policy

`keeper/policy.go` は LLM を呼ばず、`RiskReport` と `Params` のみから action を計算する。

```text
max_score = max(report.risk_score for reports)
categories = union(report.risk_categories for reports)

if categories does not intersect high_risk_categories:
  return WATCH if max_score >= watch_threshold else NONE

if max_score >= revert_threshold:
  return REVERT_TRANSFER
if max_score >= freeze_threshold:
  return FREEZE_ADDRESS
if max_score >= block_threshold:
  return BLOCK_TX
if max_score >= watch_threshold:
  return WATCH
return NONE
```

`REVERT_TRANSFER` は post-finality のみで使う。pre-finality の tx に対しては `BLOCK_TX` に downcast する。

## 9. Quorum Calculation

投票権重み付き quorum を採用する。

```text
approve_power = sum(active_agent.voting_power for approve votes)
reject_power = sum(active_agent.voting_power for reject votes)
total_power = sum(active_agent.voting_power for active agents)

approve_ratio = approve_power / total_power
reject_ratio = reject_power / total_power
```

- `ceil(approve_power * 100 / total_power) >= quorum_threshold` で `APPROVED`
- `ceil(reject_power * 100 / total_power) >= quorum_threshold` で `REJECTED`
- `current_height > voting_deadline_height` で quorum 未達なら `EXPIRED`
- `REVERT_TRANSFER` は params により全 active agent の approve を要求できる

## 10. Proposal Filtering Integration

### 10.1 CheckTx

Tx hash が `ActiveTxSanction` に存在する場合、mempool 受理を拒否する。`CheckTx` は liveness 補助であり、最終的な安全性は `ProcessProposal` で担保する。

### 10.2 PrepareProposal

提案者は候補 tx を走査し、`ActiveTxSanction/{tx_hash}` に存在する tx を除外する。除外結果は event/log に残す。

### 10.3 ProcessProposal

全バリデータは提案ブロック内の tx hash を計算し、`ActiveTxSanction` に存在する tx が含まれていれば proposal を reject する。

Important constraints:

- external API を呼ばない。
- LLM を呼ばない。
- tx hash 計算と store lookup だけを行う。
- map iteration に依存しない。

## 11. Agent Service

### 11.1 Configuration

```yaml
chain:
  rpc_url: "http://127.0.0.1:26657"
  grpc_url: "127.0.0.1:9090"
  chain_id: "sanction-demo-1"

agent:
  agent_id: "validator-1-agent"
  signer_key_name: "agent1"
  validator_operator_address: "cosmosvaloper..."

risk_service:
  endpoint: "http://127.0.0.1:8081"
  source: "chainalysis-mock"
  timeout_ms: 1000

llm:
  endpoint: "http://127.0.0.1:11434"
  provider: "ollama"
  model: "llama3.1"
  timeout_ms: 5000

policy:
  version: "poc-v1"
  local_dry_run: false
```

### 11.2 Agent Loop

```text
1. Subscribe to pending tx or poll mempool.
2. Decode tx and extract transfer-like messages.
3. Query risk service for sender and recipient.
4. If score < watch_threshold, skip or log.
5. Send structured prompt to central local LLM.
6. Store LLM response locally and compute rationale hash.
7. Build RiskReport.
8. Submit MsgSubmitRiskReport.
9. Open or join SanctionCase.
10. Sign and submit SanctionVote.
11. Watch case status and optionally trigger MsgExecuteSanction.
```

### 11.3 Structured LLM Prompt

LLM prompt は自然文ではなく、JSON に近い構造化入力を使う。

```json
{
  "task": "risk_explanation",
  "policy_version": "poc-v1",
  "tx": {
    "hash": "...",
    "sender": "...",
    "recipient": "...",
    "amount": "1000000",
    "denom": "utoken"
  },
  "risk": {
    "source": "chainalysis-mock",
    "score": 92,
    "categories": ["scam", "money_laundering"],
    "exposure_type": "direct"
  },
  "required_output": {
    "recommended_action": "one of none, watch, block, freeze, escrow, revert",
    "rationale": "short explanation",
    "caveats": "uncertainty and missing evidence"
  }
}
```

Agent は LLM の `recommended_action` をそのまま信頼せず、chain と同じ deterministic policy をローカルにも実装して照合する。

## 12. Mock Risk Service

PoC の mock risk service は次の API を提供する。

```text
GET /v1/address/{address}/risk
POST /v1/tx/screen
```

Example response:

```json
{
  "source": "chainalysis-mock",
  "response_id": "risk_001",
  "address": "cosmos1...",
  "risk_score": 92,
  "risk_categories": ["scam", "money_laundering"],
  "exposure_type": "direct",
  "updated_at": "2026-06-16T00:00:00Z"
}
```

## 13. Events

```text
EventRiskReportSubmitted(report_id, tx_hash, risk_score, source)
EventSanctionCaseOpened(case_id, tx_hash, action)
EventSanctionVoteSubmitted(case_id, agent_id, option, action)
EventSanctionApproved(case_id, action, approve_power, total_power)
EventSanctionRejected(case_id, reject_power, total_power)
EventTxBlocked(case_id, tx_hash)
EventAddressFrozen(case_id, address)
EventSanctionExecuted(case_id, action, result_code)
EventSanctionRevoked(case_id, reason)
```

## 14. Error Codes

```text
ErrAgentNotFound
ErrAgentInactive
ErrDuplicateAgent
ErrRiskReportNotFound
ErrRiskReportExpired
ErrInvalidRiskScore
ErrSourceNotAccepted
ErrCaseNotFound
ErrDuplicateCase
ErrCaseNotPending
ErrVoteAlreadySubmitted
ErrInvalidSignature
ErrQuorumNotReached
ErrActionNotAllowed
ErrTxAlreadySanctioned
ErrAddressAlreadyFrozen
ErrExecutionUnsupported
ErrUnauthorized
```

## 15. Acceptance Criteria

MVP は次の条件を満たせば完了とする。

- 3 つ以上の agent を genesis または tx で登録できる。
- mock risk service が high-risk address を返せる。
- local LLM service から説明文を取得し、その hash を risk report に保存できる。
- suspicious tx に対して `RiskReport` を提出できる。
- `SanctionCase` を開き、複数 agent が署名付き vote を提出できる。
- quorum 到達により case が `APPROVED` になる。
- approved `BLOCK_TX` case が `ActiveTxSanction` に反映される。
- `PrepareProposal` で対象 tx を除外できる。
- `ProcessProposal` で対象 tx を含む proposal を reject できる。
- approved `FREEZE_ADDRESS` case により address を凍結できる。
- query API で report、case、vote、execution record を確認できる。
- unit test で policy、signature、quorum、duplicate vote、expired report、proposal filtering を検証できる。

## 16. Non-goals for PoC

- 実 Chainalysis API との本番接続。
- 完全分散 LLM による独立判断。
- パブリックチェーン全体での不可逆な履歴削除。
- IBC transfer の完全巻き戻し。
- 法的な制裁対象判定の自動確定。
- 個人情報を含む KYC データのオンチェーン保存。

## 17. Implementation Order

1. `proto/sanction/v1` の追加。
2. `x/sanction/types` と genesis validation。
3. keeper store、query、msg server。
4. deterministic policy と quorum calculation。
5. signature verification。
6. `BLOCK_TX` active sanction。
7. app integration for `CheckTx`、`PrepareProposal`、`ProcessProposal`。
8. mock risk service。
9. central local LLM adapter。
10. sanction agent CLI。
11. `FREEZE_ADDRESS` と execution record。
12. evaluation scripts。

## 18. Open Questions

- `REVERT_TRANSFER` を bank module 互換の補償 transfer に限定するか、専用 token module を作るか。
- agent voting power を validator power と同期するか、別パラメータとして管理するか。
- quorum を全員署名、2/3、または action ごとの個別設定にするか。
- LLM rationale 全文をどこに保存するか。ローカルファイル、IPFS、DB、または保存しない方針にするか。
- 異議申し立て、制裁解除、誤検知時補償を governance module に接続するか。
