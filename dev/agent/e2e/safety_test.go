package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/yanagihalab/G503/dev/agent/internal/agentapi"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	"github.com/yanagihalab/G503/dev/agent/internal/ownership"
	"github.com/yanagihalab/G503/dev/agent/internal/riskclient"
	"github.com/yanagihalab/G503/dev/agent/internal/signer"
	"github.com/yanagihalab/G503/x/sanction/keeper"
	st "github.com/yanagihalab/G503/x/sanction/types"
)

// Opt-in tests use real signed transactions on the five-validator shell fixture.
type safetyLab struct {
	t                           *testing.T
	ctx                         context.Context
	home, rpcURL, apiURL, chain string
	rpc                         *chainclient.Client
	keys                        []map[string]safetyKey
	owners                      map[string]*ownership.Session
}

type safetyKey struct {
	Address string `json:"address"`
	Key     string `json:"private_key_hex"`
}

func lab(t *testing.T) *safetyLab {
	t.Helper()
	home := os.Getenv("G503_SAFETY_HOME")
	if home == "" {
		t.Skip("run dev/scripts/run-sanction-safety-e2e.sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	l := &safetyLab{t: t, ctx: ctx, home: home, rpcURL: os.Getenv("G503_SAFETY_RPC"), apiURL: os.Getenv("G503_SAFETY_API"), chain: os.Getenv("G503_CHAIN_ID"), owners: map[string]*ownership.Session{}}
	l.rpc = chainclient.New(l.rpcURL, 10*time.Second)
	for i := 1; i <= 5; i++ {
		bz, err := os.ReadFile(filepath.Join(home, fmt.Sprintf("node%d/config/demo-keys.json", i)))
		require.NoError(t, err)
		var keys struct {
			Faucet    safetyKey `json:"faucet"`
			Demo      safetyKey `json:"demo"`
			Agent     safetyKey `json:"agent"`
			Authority safetyKey `json:"authority"`
			Validator safetyKey `json:"validator"`
		}
		require.NoError(t, json.Unmarshal(bz, &keys))
		l.keys = append(l.keys, map[string]safetyKey{"faucet": keys.Faucet, "demo": keys.Demo, "agent": keys.Agent, "authority": keys.Authority, "validator": keys.Validator})
	}
	_, err := signer.New("prefix-config", l.keys[0]["faucet"].Key)
	require.NoError(t, err)
	return l
}

func (l *safetyLab) query(client *chainclient.Client, method string, req, resp proto.Message) error {
	bz, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	bz, err = client.Query(l.ctx, "/sanction.v1.Query/"+method, bz, 0)
	if err != nil {
		return err
	}
	return proto.Unmarshal(bz, resp)
}

func (l *safetyLab) send(key string, code uint32, msgs ...sdk.Msg) string {
	l.t.Helper()
	s, err := signer.New("test", key)
	require.NoError(l.t, err)
	memo := ""
	if lease, e := ownership.Read(l.ctx, l.rpc, s.Address, 0); e == nil && lease != nil {
		owner := l.owners[s.Address]
		if owner == nil {
			owner = &ownership.Session{RPC: l.rpc, Address: s.Address, TTL: 12, Options: chainclient.SignOptions{ChainID: l.chain, PrivateKeyHex: key}}
			require.NoError(l.t, owner.Acquire(l.ctx, 90*time.Second))
			l.owners[s.Address] = owner
		}
		require.NoError(l.t, owner.Ensure(l.ctx))
		memo = owner.Memo()
	}
	account, err := l.rpc.AccountAt(l.ctx, s.Address, 0)
	require.NoError(l.t, err)
	result, err := l.rpc.BroadcastSignedMsgs(l.ctx, chainclient.SignOptions{ChainID: l.chain, PrivateKeyHex: key, AccountNumber: account.Number, Sequence: account.Sequence, GasLimit: 900000, Memo: memo}, msgs...)
	require.NoError(l.t, err, "CheckTx must accept the signed tx; the expected rejection must be on-chain")
	var tx chainclient.TxResult
	require.Eventually(l.t, func() bool { tx, err = l.rpc.LookupTx(l.ctx, result.Hash); return err == nil && tx.Found }, 45*time.Second, 150*time.Millisecond)
	require.Equal(l.t, code, tx.Code, tx.Log)
	if code != 0 {
		expected := map[uint32]string{17: "address already frozen", 19: "unauthorized", 21: "original successful transfer", 22: "original transfer already settled", 23: "approved false-positive review required"}
		require.Contains(l.t, tx.Log, expected[code])
	}
	file, err := os.OpenFile(filepath.Join(l.home, "logs/safety-transactions.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	require.NoError(l.t, err)
	require.NoError(l.t, json.NewEncoder(file).Encode(map[string]any{"hash": result.Hash, "height": tx.Height, "code": tx.Code, "log": tx.Log}))
	require.NoError(l.t, file.Close())
	return result.Hash
}

func (l *safetyLab) balance(address string) string {
	resp, err := http.Get(l.apiURL + "/cosmos/bank/v1beta1/balances/" + address + "/by_denom?denom=stake")
	require.NoError(l.t, err)
	defer resp.Body.Close()
	require.Equal(l.t, http.StatusOK, resp.StatusCode)
	var wire struct {
		Balance struct {
			Amount string `json:"amount"`
		} `json:"balance"`
	}
	require.NoError(l.t, json.NewDecoder(resp.Body).Decode(&wire))
	return wire.Balance.Amount
}

func (l *safetyLab) save(name string, value any) {
	bz, err := json.MarshalIndent(value, "", "  ")
	require.NoError(l.t, err)
	require.NoError(l.t, os.WriteFile(filepath.Join(l.home, "logs", name), bz, 0o600))
}

func TestSettlementSafety(t *testing.T) {
	l := lab(t)
	faucet, demo := l.keys[0]["faucet"], l.keys[0]["demo"]
	beforeFrom, beforeTo := l.balance(faucet.Address), l.balance(demo.Address)
	hash := l.send(faucet.Key, 0, &banktypes.MsgSend{FromAddress: faucet.Address, ToAddress: demo.Address, Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", 13))})
	hashBytes, err := hex.DecodeString(hash)
	require.NoError(t, err)
	transfers := &st.QueryOriginalTransfersResponse{}
	require.NoError(t, l.query(l.rpc, "OriginalTransfers", &st.QueryOriginalTransfersRequest{TxHash: hash}, transfers))
	require.Len(t, transfers.Transfers, 1)
	require.Equal(t, "13", transfers.Transfers[0].Amount)
	status, err := l.rpc.Status(l.ctx)
	require.NoError(t, err)
	r := &st.RiskReport{ReportId: "safety-original", TxHash: hashBytes, Sender: faucet.Address, Recipient: demo.Address, Amount: "13", Denom: "stake", Source: "chainalysis-mock", RiskScore: 95, RiskCategories: []string{"scam"}, ObservedHeight: status.Height, ExpiryHeight: status.Height + 250, EvidenceHash: bytes.Repeat([]byte{1}, 32), Submitter: faucet.Address}
	open := func(id string, report *st.RiskReport) *st.MsgOpenSanctionCase {
		return &st.MsgOpenSanctionCase{Submitter: faucet.Address, CaseId: id, TxHash: report.TxHash, TargetAddress: report.Recipient, TargetType: st.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS, RequestedAction: st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER, ReportIds: []string{report.ReportId}}
	}
	for i, mutate := range []func(*st.RiskReport){func(r *st.RiskReport) { r.Amount = "14" }, func(r *st.RiskReport) { r.TxHash = bytes.Repeat([]byte{0xff}, 32) }} {
		bad := proto.Clone(r).(*st.RiskReport)
		bad.ReportId = fmt.Sprintf("safety-forged-%d", i)
		mutate(bad)
		l.send(faucet.Key, 21, &st.MsgSubmitRiskReport{Submitter: faucet.Address, Report: bad}, open(bad.ReportId, bad))
	}
	// Both cases are opened before the first recovery, so execution-time dedup is tested.
	l.send(faucet.Key, 0, &st.MsgSubmitRiskReport{Submitter: faucet.Address, Report: r}, open("safety-first", r), open("safety-second", r))
	for i, keys := range l.keys {
		s, err := signer.New(fmt.Sprintf("validator-%d", i+1), keys["agent"].Key)
		require.NoError(t, err)
		var msgs []sdk.Msg
		for _, id := range []string{"safety-first", "safety-second"} {
			v, err := s.SignVote(l.chain, &st.SanctionVote{CaseId: id, Option: st.VoteOption_VOTE_OPTION_APPROVE, ApprovedAction: st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER, SignedHeight: status.Height, ReasonCode: "adversarial_duplicate_case_test"})
			require.NoError(t, err)
			msgs = append(msgs, &st.MsgSubmitSanctionVote{Submitter: s.Address, Vote: v})
		}
		l.send(keys["agent"].Key, 0, msgs...)
	}
	l.send(faucet.Key, 0, &st.MsgExecuteSanction{Executor: faucet.Address, CaseId: "safety-first"})
	l.send(faucet.Key, 22, &st.MsgExecuteSanction{Executor: faucet.Address, CaseId: "safety-second"})
	l.send(faucet.Key, 22, open("safety-third", r))
	require.Equal(t, beforeFrom, l.balance(faucet.Address))
	require.Equal(t, beforeTo, l.balance(demo.Address))
	for _, id := range []string{"safety-first", "safety-second"} {
		votes := &st.QuerySanctionVotesResponse{}
		require.NoError(t, l.query(l.rpc, "SanctionVotes", &st.QuerySanctionVotesRequest{CaseId: id}, votes))
		require.Len(t, votes.Votes, 5)
	}
	l.save("safety-settlement-state.json", map[string]any{"tx_hash": hash, "sender_balance": beforeFrom, "recipient_balance": beforeTo, "approved_cases": 2, "votes_per_case": 5, "duplicate_execution_rejected": true, "duplicate_open_rejected": true, "forged_original_rejected": true})
}

func TestFreezeReviewRevoke(t *testing.T) {
	l := lab(t)
	faucet, demo, authority := l.keys[0]["faucet"], l.keys[0]["demo"], l.keys[0]["authority"]
	auth := &st.QueryAuthorityResponse{}
	require.NoError(t, l.query(l.rpc, "Authority", &st.QueryAuthorityRequest{}, auth))
	require.Equal(t, authority.Address, auth.Authority)
	hash := l.send(faucet.Key, 0, &banktypes.MsgSend{FromAddress: faucet.Address, ToAddress: demo.Address, Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", 3))})
	id := "auto-" + strings.ToUpper(hash)
	frozen := &st.QueryFrozenAddressResponse{}
	require.Eventually(t, func() bool {
		return l.query(l.rpc, "FrozenAddress", &st.QueryFrozenAddressRequest{Address: demo.Address}, frozen) == nil && frozen.Freeze != nil && frozen.Freeze.CaseId == id
	}, 100*time.Second, 200*time.Millisecond)
	// All five resident agents must independently evaluate the case before review.
	for i := 1; i <= 5; i++ {
		path := filepath.Join(l.home, fmt.Sprintf("logs/agent%d-safety.jsonl", i))
		require.Eventually(t, func() bool {
			bz, _ := os.ReadFile(path)
			for _, line := range bytes.Split(bz, []byte("\n")) {
				if bytes.Contains(line, []byte(`"case_id":"`+id+`"`)) && bytes.Contains(line, []byte(`"event":"reviewed"`)) {
					return true
				}
			}
			return false
		}, 30*time.Second, 200*time.Millisecond)
	}
	resume := &banktypes.MsgSend{FromAddress: demo.Address, ToAddress: faucet.Address, Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", 1))}
	failedHash := l.send(demo.Key, 17, resume)
	failedReceipts := &st.QueryOriginalTransfersResponse{}
	require.NoError(t, l.query(l.rpc, "OriginalTransfers", &st.QueryOriginalTransfersRequest{TxHash: failedHash}, failedReceipts))
	require.Empty(t, failedReceipts.Transfers)
	l.send(faucet.Key, 19, &st.MsgRevokeSanction{Authority: faucet.Address, CaseId: id, Reason: "not authorized"})
	// Claiming the right authority in the message is insufficient without its key.
	account, err := l.rpc.AccountAt(l.ctx, authority.Address, 0)
	require.NoError(t, err)
	spoof, err := l.rpc.BroadcastSignedMsgs(l.ctx, chainclient.SignOptions{ChainID: l.chain, PrivateKeyHex: faucet.Key, AccountNumber: account.Number, Sequence: account.Sequence, GasLimit: 900000}, &st.MsgRevokeSanction{Authority: authority.Address, CaseId: id, Reason: "spoofed authority"})
	require.Error(t, err)
	require.NotZero(t, spoof.Code)
	l.send(authority.Key, 23, &st.MsgRevokeSanction{Authority: authority.Address, CaseId: id, Reason: "no review"})
	c := &st.QuerySanctionCaseResponse{}
	require.NoError(t, l.query(l.rpc, "SanctionCase", &st.QuerySanctionCaseRequest{CaseId: id}, c))
	v := &st.QuerySanctionVotesResponse{}
	require.NoError(t, l.query(l.rpc, "SanctionVotes", &st.QuerySanctionVotesRequest{CaseId: id}, v))
	require.GreaterOrEqual(t, len(v.Votes), 4)
	original := &st.QueryOriginalTransfersResponse{}
	require.NoError(t, l.query(l.rpc, "OriginalTransfers", &st.QueryOriginalTransfersRequest{TxHash: hash}, original))
	// A corrected mock source drives five independent signed re-evaluations.
	corrected, err := riskclient.New(os.Getenv("G503_REVIEW_RISK_URL"), 5*time.Second).ScreenTx(l.ctx, riskclient.TxScreenRequest{TxHash: hash, Sender: faucet.Address, Recipient: demo.Address, Amount: "3", Denom: "stake"})
	require.NoError(t, err)
	require.Less(t, corrected.RiskScore, uint32(60))
	api, err := agentapi.New(os.Getenv("G503_REVIEW_AGENT_API"), os.Getenv("G503_AGENT_API_TOKEN"), "validator-agent", 5*time.Second, agentapi.TLSConfig{})
	require.NoError(t, err)
	params := &st.QueryParamsResponse{}
	require.NoError(t, l.query(l.rpc, "Params", &st.QueryParamsRequest{}, params))
	p := params.Params
	status, err := l.rpc.Status(l.ctx)
	require.NoError(t, err)
	type reEvaluation struct {
		Decision agentapi.EvaluateResponse `json:"decision"`
		Report   *st.RiskReport            `json:"signed_report"`
	}
	var reviews []reEvaluation
	for i, keys := range l.keys {
		id := fmt.Sprintf("validator-%d", i+1)
		decision, raw, err := api.Evaluate(l.ctx, agentapi.EvaluateRequest{AgentID: id, TargetType: "address", Tx: agentapi.TxInput{TxHash: hash, Sender: faucet.Address, Recipient: demo.Address, Amount: "3", Denom: "stake", Confirmed: true}, Risk: agentapi.RiskInput{Source: corrected.Source, ResponseID: corrected.ResponseID, Address: demo.Address, Score: corrected.RiskScore, Categories: corrected.RiskCategories, ExposureType: corrected.ExposureType}, Policy: agentapi.PolicyInput{WatchThreshold: p.WatchThreshold, BlockThreshold: p.BlockThreshold, FreezeThreshold: p.FreezeThreshold, RevertThreshold: p.RevertThreshold, HighRisk: p.HighRiskCategories}})
		require.NoError(t, err)
		require.Equal(t, "none", decision.Action)
		require.Equal(t, "reject", decision.VoteOption)
		digest := sha256.Sum256(raw)
		s, err := signer.New(id, keys["agent"].Key)
		require.NoError(t, err)
		report, err := s.SignRiskReport(l.chain, &st.RiskReport{ReportId: "review-" + hash + "-" + id, TxHash: original.Transfers[0].TxHash, Sender: faucet.Address, Recipient: demo.Address, Amount: "3", Denom: "stake", Source: corrected.Source, SourceResponseId: corrected.ResponseID + "|" + decision.DecisionID, RiskScore: decision.CalibratedScore, RiskCategories: corrected.RiskCategories, ObservedHeight: status.Height, ExpiryHeight: status.Height + 100, EvidenceHash: digest[:]})
		require.NoError(t, err)
		reviews = append(reviews, reEvaluation{Decision: decision, Report: report})
	}
	evidence, err := json.Marshal(map[string]any{"case": c, "votes": v, "original": original, "corrected_risk": corrected, "re_evaluations": reviews, "finding": "operator accepts five signed no-sanction re-evaluations; release address"})
	require.NoError(t, err)
	evidenceHash := sha256.Sum256(evidence)
	require.NoError(t, os.WriteFile(filepath.Join(l.home, "logs/safety-review-evidence.json"), evidence, 0o600))
	l.send(faucet.Key, 19, &st.MsgReviewSanction{Authority: faucet.Address, CaseId: id, Reason: "unauthorized review", EvidenceHash: evidenceHash[:]})
	l.send(authority.Key, 0, &st.MsgReviewSanction{Authority: authority.Address, CaseId: id, Reason: "false-positive accepted after operator review", EvidenceHash: evidenceHash[:]})
	require.NoError(t, l.query(l.rpc, "FrozenAddress", &st.QueryFrozenAddressRequest{Address: demo.Address}, &st.QueryFrozenAddressResponse{}), "review alone must keep freeze")
	l.send(authority.Key, 0, &st.MsgRevokeSanction{Authority: authority.Address, CaseId: id, Reason: "approved review; restore ordinary transfers"})
	l.send(demo.Key, 0, resume)
	l.save("safety-review-state.json", map[string]any{"case_id": id, "evidence_hash_hex": hex.EncodeToString(evidenceHash[:]), "signed_re_evaluations": len(reviews), "blocked_transfer_hash": failedHash, "normal_transfer_resumed": true, "unauthorized_rejected": true, "review_required": true})
}

func TestSafetyPersistence(t *testing.T) {
	l := lab(t)
	var settlement struct {
		TxHash           string `json:"tx_hash"`
		SenderBalance    string `json:"sender_balance"`
		RecipientBalance string `json:"recipient_balance"`
	}
	bz, err := os.ReadFile(filepath.Join(l.home, "logs/safety-settlement-state.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(bz, &settlement))
	var review struct {
		CaseID       string `json:"case_id"`
		EvidenceHash string `json:"evidence_hash_hex"`
	}
	bz, err = os.ReadFile(filepath.Join(l.home, "logs/safety-review-state.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(bz, &review))
	evidence, err := os.ReadFile(filepath.Join(l.home, "logs/safety-review-evidence.json"))
	require.NoError(t, err)
	digest := sha256.Sum256(evidence)
	require.Equal(t, review.EvidenceHash, hex.EncodeToString(digest[:]))
	var preserved struct {
		Reviews []struct {
			Decision agentapi.EvaluateResponse `json:"decision"`
			Report   *st.RiskReport            `json:"signed_report"`
		} `json:"re_evaluations"`
	}
	require.NoError(t, json.Unmarshal(evidence, &preserved))
	require.Len(t, preserved.Reviews, 5)
	for i, r := range preserved.Reviews {
		s, err := signer.New(fmt.Sprintf("validator-%d", i+1), l.keys[i]["agent"].Key)
		require.NoError(t, err)
		require.NotNil(t, r.Report)
		require.Equal(t, s.AgentID, r.Decision.AgentID)
		require.Equal(t, s.Address, r.Report.Submitter)
		require.Equal(t, "none", r.Decision.Action)
		require.True(t, keeper.VerifySecp256k1(s.PublicKey(), r.Report.SubmitterSignature, keeper.RiskReportSignBytes(l.chain, r.Report)))
		raw, err := json.Marshal(r.Decision)
		require.NoError(t, err)
		decisionHash := sha256.Sum256(raw)
		require.Equal(t, decisionHash[:], r.Report.EvidenceHash)
	}
	basePort := 0
	_, err = fmt.Sscanf(strings.TrimPrefix(l.rpcURL, "http://127.0.0.1:"), "%d", &basePort)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		rpc := chainclient.New(fmt.Sprintf("http://127.0.0.1:%d", basePort+i), 10*time.Second)
		original := &st.QueryOriginalTransfersResponse{}
		require.NoError(t, l.query(rpc, "OriginalTransfers", &st.QueryOriginalTransfersRequest{TxHash: settlement.TxHash}, original))
		require.Len(t, original.Transfers, 1)
		s := &st.QuerySettlementsResponse{}
		require.NoError(t, l.query(rpc, "Settlements", &st.QuerySettlementsRequest{TxHash: settlement.TxHash}, s))
		require.Len(t, s.Settlements, 1)
		require.Equal(t, "safety-first", s.Settlements[0].CaseId)
		r := &st.QueryReviewResponse{}
		require.NoError(t, l.query(rpc, "Review", &st.QueryReviewRequest{CaseId: review.CaseID}, r))
		require.Equal(t, review.EvidenceHash, hex.EncodeToString(r.Review.EvidenceHash))
		require.Positive(t, r.Review.RevokedHeight)
		c := &st.QuerySanctionCaseResponse{}
		require.NoError(t, l.query(rpc, "SanctionCase", &st.QuerySanctionCaseRequest{CaseId: review.CaseID}, c))
		require.Equal(t, st.CaseStatus_CASE_STATUS_REVOKED, c.SanctionCase.Status)
		require.Error(t, l.query(rpc, "FrozenAddress", &st.QueryFrozenAddressRequest{Address: l.keys[0]["demo"].Address}, &st.QueryFrozenAddressResponse{}))
	}
	l.send(l.keys[0]["faucet"].Key, 22, &st.MsgExecuteSanction{Executor: l.keys[0]["faucet"].Address, CaseId: "safety-second"})
	// Exercise the new REST routes, not only ABCI queries.
	for _, path := range []string{"authority", "tx/" + settlement.TxHash + "/transfers", "tx/" + settlement.TxHash + "/settlements", "cases/" + review.CaseID + "/review"} {
		resp, err := http.Get(l.apiURL + "/sanction/v1/" + path)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, resp.Body)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
	}
	l.save("safety-metrics.json", map[string]any{"validators": 5, "agents": 5, "signed_re_evaluations": len(preserved.Reviews), "forged_original_rejected": true, "duplicate_settlement_rejected": true, "duplicate_rejected_after_all_node_restart": true, "freeze_review_revoke_resume": true, "audit_persisted_on_all_nodes": true, "services": "mock risk/LLM/Agent API; authority-attested operator review"})
}
