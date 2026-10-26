package e2e

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	"github.com/yanagihalab/G503/dev/agent/internal/ownership"
	"github.com/yanagihalab/G503/dev/agent/internal/signer"
	"github.com/yanagihalab/G503/x/sanction/keeper"
	st "github.com/yanagihalab/G503/x/sanction/types"
	"google.golang.org/protobuf/proto"
)

type churnCheckpoint struct {
	Case        *st.SanctionCase `json:"case"`
	Leases      []*st.AgentLease `json:"leases"`
	FromBalance string           `json:"from_balance"`
	ToBalance   string           `json:"to_balance"`
}

func churnSend(l *safetyLab, key, memo string, code uint32, msgs ...sdk.Msg) string {
	l.t.Helper()
	s, err := signer.New("churn", key)
	require.NoError(l.t, err)
	a, err := l.rpc.AccountAt(l.ctx, s.Address, 0)
	require.NoError(l.t, err)
	r, err := l.rpc.BroadcastSignedMsgs(l.ctx, chainclient.SignOptions{ChainID: l.chain, PrivateKeyHex: key, AccountNumber: a.Number, Sequence: a.Sequence, GasLimit: 900000, Memo: memo}, msgs...)
	require.NoError(l.t, err)
	var result chainclient.TxResult
	require.Eventually(l.t, func() bool { result, err = l.rpc.LookupTx(l.ctx, r.Hash); return err == nil && result.Found }, 45*time.Second, 150*time.Millisecond)
	require.Equal(l.t, code, result.Code, result.Log)
	return r.Hash
}

func churnVote(t *testing.T, l *safetyLab, key string, c *st.SanctionCase, id string) *st.MsgSubmitSanctionVote {
	s, err := signer.New(id, key)
	require.NoError(t, err)
	h, err := l.rpc.Status(l.ctx)
	require.NoError(t, err)
	v, err := s.SignVote(l.chain, &st.SanctionVote{CaseId: c.CaseId, Option: st.VoteOption_VOTE_OPTION_APPROVE, ApprovedAction: c.RequestedAction, SignedHeight: h.Height})
	require.NoError(t, err)
	return &st.MsgSubmitSanctionVote{Submitter: s.Address, Vote: v}
}

func TestOwnershipAndSnapshotChurn(t *testing.T) {
	phase := os.Getenv("G503_OWNERSHIP_PHASE")
	if phase == "" {
		t.Skip("run run-ownership-snapshot-e2e.sh")
	}
	l := lab(t)
	if phase == "policy" {
		p := &st.QueryParamsResponse{}
		require.NoError(t, l.query(l.rpc, "Params", &st.QueryParamsRequest{}, p))
		p.Params.EvidenceTtlBlocks = 300
		p.Params.VotingPeriodBlocks = 100
		a := l.keys[0]["authority"]
		churnSend(l, a.Key, "", 0, &st.MsgUpdateParams{Authority: a.Address, Params: p.Params})
		return
	}
	if phase == "send" {
		faucet, demo := l.keys[0]["faucet"], l.keys[0]["demo"]
		cp := churnCheckpoint{FromBalance: l.balance(faucet.Address), ToBalance: l.balance(demo.Address)}
		hash := churnSend(l, faucet.Key, "", 0, &banktypes.MsgSend{FromAddress: faucet.Address, ToAddress: demo.Address, Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", 17))})
		bz, err := hex.DecodeString(hash)
		require.NoError(t, err)
		cp.Case = &st.SanctionCase{CaseId: "auto-" + hash, TxHash: bz}
		l.save("ownership-checkpoint.json", cp)
		return
	}
	var cp churnCheckpoint
	recoveryRead(t, l.home, "ownership-checkpoint.json", &cp)
	if phase == "prepare" {
		response := &st.QuerySanctionCaseResponse{}
		require.NoError(t, l.query(l.rpc, "SanctionCase", &st.QuerySanctionCaseRequest{CaseId: cp.Case.CaseId}, response))
		cp.Case = response.SanctionCase
		require.Len(t, cp.Case.Voters, 5)
		require.Equal(t, st.CaseStatus_CASE_STATUS_APPROVED, cp.Case.Status)
		votes := &st.QuerySanctionVotesResponse{}
		require.NoError(t, l.query(l.rpc, "SanctionVotes", &st.QuerySanctionVotesRequest{CaseId: cp.Case.CaseId}, votes))
		require.Len(t, votes.Votes, 4)
		for _, v := range votes.Votes {
			require.Equal(t, st.VoteOption_VOTE_OPTION_APPROVE, v.Option)
			require.Equal(t, "independent_policy_review", v.ReasonCode)
		}
		for i := 0; i < 5; i++ {
			key := l.keys[i]["validator"]
			lease, err := ownership.Read(l.ctx, l.rpc, key.Address, 0)
			require.NoError(t, err)
			require.NotNil(t, lease)
			cp.Leases = append(cp.Leases, lease)
		}
		l.save("ownership-checkpoint.json", cp)
		return
	}
	if phase == "unbond" {
		key := l.keys[2]["validator"]
		owner := &ownership.Session{RPC: l.rpc, Address: key.Address, TTL: 12, Options: chainclient.SignOptions{ChainID: l.chain, PrivateKeyHex: key.Key}}
		require.NoError(t, owner.Acquire(l.ctx, 90*time.Second))
		addr, err := sdk.AccAddressFromBech32(key.Address)
		require.NoError(t, err)
		churnSend(l, key.Key, owner.Memo(), 0, &stakingtypes.MsgUndelegate{DelegatorAddress: key.Address, ValidatorAddress: sdk.ValAddress(addr).String(), Amount: sdk.NewInt64Coin("stake", 1000000)})
		return
	}
	current := &st.QuerySanctionCaseResponse{}
	require.NoError(t, l.query(l.rpc, "SanctionCase", &st.QuerySanctionCaseRequest{CaseId: cp.Case.CaseId}, current))
	for i := range cp.Case.Voters {
		require.True(t, proto.Equal(cp.Case.Voters[i], current.SanctionCase.Voters[i]))
	}
	require.Equal(t, cp.Case.QuorumThreshold, current.SanctionCase.QuorumThreshold)
	require.Equal(t, cp.Case.UnanimousRequiredForRevert, current.SanctionCase.UnanimousRequiredForRevert)
	if phase == "changed" {
		churnSend(l, l.keys[0]["faucet"].Key, "", st.ErrQuorumNotReached.ABCICode(), &st.MsgExecuteSanction{Executor: l.keys[0]["faucet"].Address, CaseId: cp.Case.CaseId})
		bz, err := os.ReadFile(filepath.Join(l.home, "node6/config/demo-keys.json"))
		require.NoError(t, err)
		var six struct {
			Validator safetyKey `json:"validator"`
		}
		require.NoError(t, json.Unmarshal(bz, &six))
		agents := &st.QueryAgentsResponse{}
		require.NoError(t, l.query(l.rpc, "Agents", &st.QueryAgentsRequest{}, agents))
		newID := ""
		active := 0
		for _, a := range agents.Agents {
			if a.Active {
				active++
			}
			if a.SignerAddress == six.Validator.Address {
				newID = a.AgentId
			}
		}
		require.Equal(t, 4, active)
		require.NotEmpty(t, newID)
		churnSend(l, six.Validator.Key, "", st.ErrAgentNotFound.ABCICode(), churnVote(t, l, six.Validator.Key, cp.Case, newID))
		h, err := l.rpc.Status(l.ctx)
		require.NoError(t, err)
		r := &st.RiskReport{ReportId: "churn-fresh-set", TxHash: cp.Case.TxHash, Recipient: cp.Case.TargetAddress, Source: "chainalysis-mock", RiskScore: 65, ObservedHeight: h.Height, ExpiryHeight: h.Height + 100, EvidenceHash: bytes.Repeat([]byte{2}, 32)}
		faucet := l.keys[0]["faucet"]
		churnSend(l, faucet.Key, "", 0, &st.MsgSubmitRiskReport{Submitter: faucet.Address, Report: r}, &st.MsgOpenSanctionCase{Submitter: faucet.Address, CaseId: "churn-new-snapshot", TxHash: r.TxHash, TargetAddress: r.Recipient, TargetType: st.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS, RequestedAction: st.SanctionAction_SANCTION_ACTION_WATCH, ReportIds: []string{r.ReportId}})
		next := &st.QuerySanctionCaseResponse{}
		require.NoError(t, l.query(l.rpc, "SanctionCase", &st.QuerySanctionCaseRequest{CaseId: "churn-new-snapshot"}, next))
		require.Len(t, next.SanctionCase.Voters, 4)
		l.save("ownership-new-snapshot.json", next)
		return
	}
	require.Equal(t, "final", phase)
	require.Equal(t, st.CaseStatus_CASE_STATUS_EXECUTED, current.SanctionCase.Status)
	votes := &st.QuerySanctionVotesResponse{}
	require.NoError(t, l.query(l.rpc, "SanctionVotes", &st.QuerySanctionVotesRequest{CaseId: cp.Case.CaseId}, votes))
	require.Len(t, votes.Votes, 5)
	for _, v := range votes.Votes {
		require.Equal(t, "independent_policy_review", v.ReasonCode)
		verified := false
		for _, a := range cp.Case.Voters {
			if a.AgentId == v.AgentId {
				verified = keeper.VerifySecp256k1(a.PublicKey, v.Signature, keeper.VoteSignBytes(l.chain, v))
			}
		}
		require.True(t, verified)
	}
	s := &st.QuerySettlementsResponse{}
	require.NoError(t, l.query(l.rpc, "Settlements", &st.QuerySettlementsRequest{TxHash: hex.EncodeToString(cp.Case.TxHash)}, s))
	require.Len(t, s.Settlements, 1)
	require.Equal(t, cp.FromBalance, l.balance(l.keys[0]["faucet"].Address))
	require.Equal(t, cp.ToBalance, l.balance(l.keys[0]["demo"].Address))
	for i, old := range cp.Leases {
		lease, err := ownership.Read(l.ctx, l.rpc, old.SignerAddress, 0)
		require.NoError(t, err)
		require.Greater(t, lease.Epoch, old.Epoch)
		require.NotEqual(t, old.HolderId, lease.HolderId)
		key := l.keys[i]["validator"]
		a, err := l.rpc.AccountAt(l.ctx, key.Address, 0)
		require.NoError(t, err)
		msg := &st.MsgExecuteSanction{Executor: key.Address, CaseId: cp.Case.CaseId}
		for _, memo := range []string{"", keeper.LeaseMemo(old)} {
			result, err := l.rpc.BroadcastSignedMsgs(l.ctx, chainclient.SignOptions{ChainID: l.chain, PrivateKeyHex: key.Key, AccountNumber: a.Number, Sequence: a.Sequence, GasLimit: 700000, Memo: memo}, msg)
			require.Error(t, err)
			require.NotZero(t, result.Code)
			require.Contains(t, result.Log, "lease fencing")
		}
		for node := 1; node <= 6; node++ {
			client := chainclient.New(fmt.Sprintf("http://127.0.0.1:%d", mustPort(t, "G503_BASE_RPC_PORT")+node-1), 10*time.Second)
			r := &st.QuerySanctionCaseResponse{}
			require.NoError(t, l.query(client, "SanctionCase", &st.QuerySanctionCaseRequest{CaseId: cp.Case.CaseId}, r))
			require.True(t, proto.Equal(current.SanctionCase, r.SanctionCase))
			persisted, err := ownership.Read(l.ctx, client, old.SignerAddress, 0)
			require.NoError(t, err)
			require.True(t, proto.Equal(lease, persisted))
		}
	}
	next := &st.QuerySanctionCaseResponse{}
	require.NoError(t, l.query(l.rpc, "SanctionCase", &st.QuerySanctionCaseRequest{CaseId: "churn-new-snapshot"}, next))
	require.Len(t, next.SanctionCase.Voters, 4)
	status, err := l.rpc.Status(l.ctx)
	require.NoError(t, err)
	executions := 0
	client := &http.Client{Timeout: 5 * time.Second}
	for h := uint64(1); h <= status.Height; h++ {
		resp, err := client.Get(fmt.Sprintf("%s/block_results?height=%d", l.rpcURL, h))
		require.NoError(t, err)
		var block struct {
			Result struct {
				Txs []struct {
					Code   uint32 `json:"code"`
					Events []struct {
						Type       string                        `json:"type"`
						Attributes []struct{ Key, Value string } `json:"attributes"`
					} `json:"events"`
				} `json:"txs_results"`
			} `json:"result"`
		}
		err = json.NewDecoder(resp.Body).Decode(&block)
		resp.Body.Close()
		require.NoError(t, err)
		for _, tx := range block.Result.Txs {
			if tx.Code != 0 {
				continue
			}
			for _, event := range tx.Events {
				if event.Type != "sanction_executed" {
					continue
				}
				for _, a := range event.Attributes {
					if a.Key == "case_id" && a.Value == cp.Case.CaseId {
						executions++
					}
				}
			}
		}
	}
	require.Equal(t, 1, executions)
	restart, err := os.ReadFile(filepath.Join(l.home, "logs/ownership-old-restart.err"))
	require.NoError(t, err)
	require.Contains(t, string(restart), "already owned")
	l.save("ownership-metrics.json", map[string]any{"initial_validators": 5, "resident_agents": 5, "automatic_agent_votes": 5, "added_validator": true, "jail_and_unbond": true, "old_case_voters": 5, "new_case_voters": 4, "cross_directory_duplicate_start_rejected": true, "old_host_fresh_restart_rejected": true, "old_processes_fenced": 5, "missing_and_stale_lease_txs_rejected": 10, "successful_settlements": 1, "successful_execution_events": executions, "duplicate_settlements": 0, "balances_restored": true, "all_six_nodes_restart": true})
}

func mustPort(t *testing.T, name string) int {
	t.Helper()
	var n int
	_, err := fmt.Sscanf(os.Getenv(name), "%d", &n)
	require.NoError(t, err)
	return n
}
