package keeper_test

import (
	"bytes"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/x/sanction/keeper"
	"github.com/yanagihalab/G503/x/sanction/types"
)

func TestOpenCaseBindsReportToTransfer(t *testing.T) {
	for _, scenario := range []string{"different-tx", "different-recipient", "unknown-target"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			report := &types.RiskReport{ReportId: "report-binding", TxHash: bytes.Repeat([]byte{0x11}, 32), Recipient: sdk.AccAddress(bytes.Repeat([]byte{0x22}, 20)).String(), RiskScore: 86, RiskCategories: []string{"scam"}, ExpiryHeight: 40}
			f.keeper.SetRiskReport(f.ctx, report)
			msg := &types.MsgOpenSanctionCase{Submitter: sdk.AccAddress(bytes.Repeat([]byte{0x33}, 20)).String(), CaseId: "case-binding", TxHash: report.TxHash, TargetAddress: report.Recipient, TargetType: types.SanctionTargetType_SANCTION_TARGET_TYPE_TX, RequestedAction: types.SanctionAction_SANCTION_ACTION_BLOCK_TX, ReportIds: []string{report.ReportId}}
			switch scenario {
			case "different-tx":
				msg.TxHash = bytes.Repeat([]byte{0x44}, 32)
			case "different-recipient":
				msg.TargetAddress = sdk.AccAddress(bytes.Repeat([]byte{0x55}, 20)).String()
			case "unknown-target":
				msg.TargetType = types.SanctionTargetType_SANCTION_TARGET_TYPE_UNSPECIFIED
				msg.RequestedAction = types.SanctionAction_SANCTION_ACTION_FREEZE_ADDRESS
			}
			_, err := f.msgServer.OpenSanctionCase(f.ctx, msg)
			require.Error(t, err)
			_, found := f.keeper.GetSanctionCase(f.ctx, msg.CaseId)
			require.False(t, found)
		})
	}
}

func TestRevokingOldCaseKeepsNewerSanctions(t *testing.T) {
	f := newFixture(t)
	hash := bytes.Repeat([]byte{0x11}, 32)
	address := sdk.AccAddress(bytes.Repeat([]byte{0x22}, 20)).String()
	f.keeper.SetSanctionCase(f.ctx, &types.SanctionCase{CaseId: "old", TxHash: hash, TargetAddress: address})
	f.keeper.SetActiveTxSanction(f.ctx, &types.ActiveSanction{CaseId: "new", TxHash: hash})
	f.keeper.SetFreezeRecord(f.ctx, &types.FreezeRecord{CaseId: "new", Address: address})
	_, err := f.msgServer.ReviewSanction(f.ctx, &types.MsgReviewSanction{Authority: authority, CaseId: "old", Reason: "review", EvidenceHash: bytes.Repeat([]byte{1}, 32)})
	require.NoError(t, err)
	_, err = f.msgServer.RevokeSanction(f.ctx.WithBlockHeight(11), &types.MsgRevokeSanction{Authority: authority, CaseId: "old", Reason: "review accepted"})
	require.NoError(t, err)
	_, found := f.keeper.GetActiveTxSanction(f.ctx, hash)
	require.True(t, found, "revoking another case removed an active sanction")
	_, found = f.keeper.GetFreezeRecord(f.ctx, address)
	require.True(t, found, "revoking another case removed a freeze")
}

func TestQuorumWithLargeVotingPowerDoesNotOverflow(t *testing.T) {
	f := newFixture(t)
	a, b := registerAgent(t, f, "large-a"), registerAgent(t, f, "large-b")
	a.info.VotingPower = ^uint64(0)
	b.info.VotingPower = ^uint64(0)
	f.keeper.SetAgent(f.ctx, a.info)
	f.keeper.SetAgent(f.ctx, b.info)
	setSnapshotCase(t, f, &types.SanctionCase{CaseId: "large-power", Status: types.CaseStatus_CASE_STATUS_PENDING, VotingDeadlineHeight: 100, RequestedAction: types.SanctionAction_SANCTION_ACTION_BLOCK_TX, TxHash: bytes.Repeat([]byte{0x66}, 32)})
	require.False(t, vote(t, f, a, "large-power").Approved)
	require.True(t, vote(t, f, b, "large-power").Approved, "unanimous votes failed because of overflow")
}

func TestMalformedTransferAmountReturnsErrorWithoutPanic(t *testing.T) {
	for _, amount := range []string{"-1", "0", "invalid"} {
		t.Run(amount, func(t *testing.T) {
			f := newFixture(t)
			f.keeper = f.keeper.WithBankKeeper(&mockBankKeeper{})
			server := keeper.NewMsgServerImpl(f.keeper)
			address := sdk.AccAddress(bytes.Repeat([]byte{0x22}, 20)).String()
			f.keeper.SetRiskReport(f.ctx, &types.RiskReport{ReportId: "bad-amount", Recipient: address, Amount: amount, Denom: "stake"})
			f.keeper.SetSanctionCase(f.ctx, &types.SanctionCase{CaseId: "bad-amount", ReportIds: []string{"bad-amount"}, Status: types.CaseStatus_CASE_STATUS_APPROVED, RequestedAction: types.SanctionAction_SANCTION_ACTION_ESCROW_FUNDS})
			require.NotPanics(t, func() {
				_, err := server.ExecuteSanction(f.ctx, &types.MsgExecuteSanction{Executor: address, CaseId: "bad-amount"})
				require.Error(t, err)
			})
		})
	}
}

func TestUnanimousRevertRequiresNonemptyValidatorSet(t *testing.T) {
	f := newFixture(t)
	f.keeper = f.keeper.WithBankKeeper(&mockBankKeeper{})
	server := keeper.NewMsgServerImpl(f.keeper)
	address := sdk.AccAddress(bytes.Repeat([]byte{0x22}, 20)).String()
	f.keeper.SetRiskReport(f.ctx, &types.RiskReport{ReportId: "empty-set", Sender: address, Recipient: address, Amount: "1", Denom: "stake"})
	f.keeper.SetSanctionCase(f.ctx, &types.SanctionCase{CaseId: "empty-set", ReportIds: []string{"empty-set"}, Status: types.CaseStatus_CASE_STATUS_APPROVED, RequestedAction: types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER})
	_, err := server.ExecuteSanction(f.ctx, &types.MsgExecuteSanction{Executor: address, CaseId: "empty-set"})
	require.Error(t, err, "missing snapshot must fail closed")
}

func TestInvalidVoteOptionDoesNotConsumeAgentVote(t *testing.T) {
	f := newFixture(t)
	agent := registerAgent(t, f, "invalid-option")
	setSnapshotCase(t, f, &types.SanctionCase{CaseId: "invalid-option", Status: types.CaseStatus_CASE_STATUS_PENDING, VotingDeadlineHeight: 100, RequestedAction: types.SanctionAction_SANCTION_ACTION_BLOCK_TX})
	msg := voteMsg(t, f, agent, "invalid-option", types.SanctionAction_SANCTION_ACTION_BLOCK_TX)
	msg.Vote.Option = types.VoteOption(255)
	sig, err := agent.priv.Sign(keeper.VoteSignBytes("sanction-test-1", msg.Vote))
	require.NoError(t, err)
	msg.Vote.Signature = sig
	_, err = f.msgServer.SubmitSanctionVote(f.ctx, msg)
	require.ErrorIs(t, err, types.ErrInvalidCase)
	_, found := f.keeper.GetSanctionVote(f.ctx, "invalid-option", agent.info.AgentId)
	require.False(t, found)
	require.True(t, vote(t, f, agent, "invalid-option").Approved)
}
