package keeper_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/yanagihalab/G503/x/sanction/keeper"
	"github.com/yanagihalab/G503/x/sanction/types"
)

func safetyReport() *types.RiskReport {
	return &types.RiskReport{ReportId: "original", TxHash: bytes.Repeat([]byte{0xaa}, 32), Sender: sdk.AccAddress(bytes.Repeat([]byte{1}, 20)).String(), Recipient: sdk.AccAddress(bytes.Repeat([]byte{2}, 20)).String(), Amount: "10", Denom: "stake", RiskScore: 95, RiskCategories: []string{"scam"}, Source: "chainalysis-mock", EvidenceHash: bytes.Repeat([]byte{3}, 32), ExpiryHeight: 50}
}

func receipt(r *types.RiskReport) *types.OriginalTransfer {
	return &types.OriginalTransfer{TxHash: r.TxHash, MessageIndex: r.MessageIndex, Sender: r.Sender, Recipient: r.Recipient, Amount: r.Amount, Denom: r.Denom, Height: 9}
}

func TestMonetaryCaseRequiresMatchingCommittedOriginal(t *testing.T) {
	for _, scenario := range []string{"absent", "same-height", "sender", "recipient", "amount", "denom", "hash", "index"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			r := safetyReport()
			tx := receipt(r)
			switch scenario {
			case "same-height":
				tx.Height = 10
			case "sender":
				tx.Sender = r.Recipient
			case "recipient":
				tx.Recipient = r.Sender
			case "amount":
				tx.Amount = "11"
			case "denom":
				tx.Denom = "uatom"
			case "hash":
				tx.TxHash = bytes.Repeat([]byte{0xbb}, 32)
			case "index":
				tx.MessageIndex = 1
			}
			if scenario != "absent" {
				f.keeper.SetOriginalTransfer(f.ctx, tx)
			}
			f.keeper.SetRiskReport(f.ctx, r)
			_, err := f.msgServer.OpenSanctionCase(f.ctx, &types.MsgOpenSanctionCase{Submitter: r.Sender, CaseId: "new", TxHash: r.TxHash, TargetAddress: r.Recipient, TargetType: types.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS, RequestedAction: types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER, ReportIds: []string{r.ReportId}})
			require.ErrorIs(t, err, types.ErrOriginalTransfer)
		})
	}
}

func TestSettlementSharedAcrossCasesAndActionsAndSurvivesRevoke(t *testing.T) {
	for _, first := range []types.SanctionAction{types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER, types.SanctionAction_SANCTION_ACTION_ESCROW_FUNDS} {
		t.Run(first.String(), func(t *testing.T) {
			f := newFixture(t)
			bank := &mockBankKeeper{}
			f.keeper = f.keeper.WithBankKeeper(bank)
			f.msgServer = keeper.NewMsgServerImpl(f.keeper)
			p := f.keeper.GetParams(f.ctx)
			p.UnanimousRequiredForRevert = false
			f.keeper.SetParams(f.ctx, p)
			r := safetyReport()
			f.keeper.SetRiskReport(f.ctx, r)
			f.keeper.SetOriginalTransfer(f.ctx, receipt(r))
			for i, action := range []types.SanctionAction{first, types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER, types.SanctionAction_SANCTION_ACTION_ESCROW_FUNDS} {
				id := fmt.Sprintf("case-%d", i)
				setSnapshotCase(t, f, &types.SanctionCase{CaseId: id, TxHash: r.TxHash, TargetAddress: r.Recipient, Status: types.CaseStatus_CASE_STATUS_APPROVED, RequestedAction: action, ReportIds: []string{r.ReportId}})
				_, err := f.msgServer.ExecuteSanction(f.ctx, &types.MsgExecuteSanction{Executor: r.Sender, CaseId: id})
				if i == 0 {
					require.NoError(t, err)
					_, err = f.msgServer.ReviewSanction(f.ctx, &types.MsgReviewSanction{Authority: authority, CaseId: id, Reason: "review accepted", EvidenceHash: bytes.Repeat([]byte{4}, 32)})
					require.NoError(t, err)
					_, err = f.msgServer.RevokeSanction(f.ctx.WithBlockHeight(11), &types.MsgRevokeSanction{Authority: authority, CaseId: id, Reason: "release"})
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, types.ErrAlreadySettled)
				}
			}
			require.Equal(t, 1, bank.sendCoinsCalls+bank.accountToModuleCalls)
			records := f.keeper.GetSettlements(f.ctx, r.TxHash)
			require.Len(t, records, 1)
			require.Equal(t, "case-0", records[0].CaseId)
			// A second message/denom in the same tx has a different identity.
			r.MessageIndex = 1
			r.ReportId = "second-message"
			f.keeper.SetOriginalTransfer(f.ctx, receipt(r))
			f.keeper.SetRiskReport(f.ctx, r)
			setSnapshotCase(t, f, &types.SanctionCase{CaseId: "other-message", TxHash: r.TxHash, TargetAddress: r.Recipient, Status: types.CaseStatus_CASE_STATUS_APPROVED, RequestedAction: first, ReportIds: []string{r.ReportId}})
			_, err := f.msgServer.ExecuteSanction(f.ctx, &types.MsgExecuteSanction{Executor: r.Sender, CaseId: "other-message"})
			require.NoError(t, err)
			require.Len(t, f.keeper.GetSettlements(f.ctx, r.TxHash), 2)
			r.Denom = "uatom"
			r.ReportId = "other-denom"
			f.keeper.SetOriginalTransfer(f.ctx, receipt(r))
			f.keeper.SetRiskReport(f.ctx, r)
			setSnapshotCase(t, f, &types.SanctionCase{CaseId: "other-denom", TxHash: r.TxHash, TargetAddress: r.Recipient, Status: types.CaseStatus_CASE_STATUS_APPROVED, RequestedAction: first, ReportIds: []string{r.ReportId}})
			_, err = f.msgServer.ExecuteSanction(f.ctx, &types.MsgExecuteSanction{Executor: r.Sender, CaseId: "other-denom"})
			require.NoError(t, err)
			require.Len(t, f.keeper.GetSettlements(f.ctx, r.TxHash), 3)
		})
	}
}

type failingSettlementBank struct{ mockBankKeeper }

func (*failingSettlementBank) SendCoins(context.Context, sdk.AccAddress, sdk.AccAddress, sdk.Coins) error {
	return fmt.Errorf("insufficient funds")
}

func TestFailedBankTransferDoesNotConsumeSettlement(t *testing.T) {
	f := newFixture(t)
	f.keeper = f.keeper.WithBankKeeper(&failingSettlementBank{})
	f.msgServer = keeper.NewMsgServerImpl(f.keeper)
	p := f.keeper.GetParams(f.ctx)
	p.UnanimousRequiredForRevert = false
	f.keeper.SetParams(f.ctx, p)
	r := safetyReport()
	f.keeper.SetRiskReport(f.ctx, r)
	f.keeper.SetOriginalTransfer(f.ctx, receipt(r))
	setSnapshotCase(t, f, &types.SanctionCase{CaseId: "failed", TxHash: r.TxHash, TargetAddress: r.Recipient, RequestedAction: types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER, Status: types.CaseStatus_CASE_STATUS_APPROVED, ReportIds: []string{r.ReportId}})
	_, err := f.msgServer.ExecuteSanction(f.ctx, &types.MsgExecuteSanction{Executor: r.Sender, CaseId: "failed"})
	require.ErrorContains(t, err, "insufficient funds")
	require.Empty(t, f.keeper.GetSettlements(f.ctx, r.TxHash))
}

func TestReviewAndRevokeRequireAuthorityAndPreserveAudit(t *testing.T) {
	f := newFixture(t)
	r := safetyReport()
	f.keeper.SetSanctionCase(f.ctx, &types.SanctionCase{CaseId: "freeze", TxHash: r.TxHash, TargetAddress: r.Recipient, RequestedAction: types.SanctionAction_SANCTION_ACTION_FREEZE_ADDRESS, Status: types.CaseStatus_CASE_STATUS_EXECUTED})
	f.keeper.SetFreezeRecord(f.ctx, &types.FreezeRecord{CaseId: "freeze", Address: r.Recipient})
	request := &types.MsgReviewSanction{Authority: r.Sender, CaseId: "freeze", Reason: "false-positive", EvidenceHash: bytes.Repeat([]byte{1}, 32)}
	_, err := f.msgServer.ReviewSanction(f.ctx, request)
	require.ErrorIs(t, err, types.ErrUnauthorized)
	_, err = f.msgServer.RevokeSanction(f.ctx, &types.MsgRevokeSanction{Authority: authority, CaseId: "freeze", Reason: "early"})
	require.ErrorIs(t, err, types.ErrReviewRequired)
	request.Authority = authority
	request.EvidenceHash = nil
	_, err = f.msgServer.ReviewSanction(f.ctx, request)
	require.Error(t, err)
	request.EvidenceHash = bytes.Repeat([]byte{1}, 32)
	_, err = f.msgServer.ReviewSanction(f.ctx, request)
	require.NoError(t, err)
	_, err = f.msgServer.ReviewSanction(f.ctx, request)
	require.Error(t, err)
	require.True(t, f.keeper.IsAddressFrozen(f.ctx, r.Recipient), "review alone must not unfreeze")
	_, err = f.msgServer.RevokeSanction(f.ctx, &types.MsgRevokeSanction{Authority: r.Sender, CaseId: "freeze", Reason: "wrong key"})
	require.ErrorIs(t, err, types.ErrUnauthorized)
	_, err = f.msgServer.RevokeSanction(f.ctx, &types.MsgRevokeSanction{Authority: authority, CaseId: "freeze", Reason: "uncommitted review"})
	require.ErrorIs(t, err, types.ErrReviewRequired)
	f.ctx = f.ctx.WithBlockHeight(11)
	_, err = f.msgServer.RevokeSanction(f.ctx, &types.MsgRevokeSanction{Authority: authority, CaseId: "freeze", Reason: "review accepted"})
	require.NoError(t, err)
	require.False(t, f.keeper.IsAddressFrozen(f.ctx, r.Recipient))
	audit, found := f.keeper.GetReview(f.ctx, "freeze")
	require.True(t, found)
	require.Equal(t, uint64(10), audit.ReviewedHeight)
	require.Equal(t, uint64(11), audit.RevokedHeight)
	require.Equal(t, request.EvidenceHash, audit.EvidenceHash)
	_, err = f.msgServer.RevokeSanction(f.ctx, &types.MsgRevokeSanction{Authority: authority, CaseId: "freeze", Reason: "duplicate"})
	require.Error(t, err)
	genesis := f.keeper.ExportGenesis(f.ctx)
	other := newFixture(t)
	other.keeper.InitGenesis(other.ctx, genesis)
	restored, _ := other.keeper.GetReview(other.ctx, "freeze")
	require.True(t, proto.Equal(audit, restored))
}

func TestSafetyGenesisRoundTripAndInvalidImports(t *testing.T) {
	f := newFixture(t)
	r := safetyReport()
	g := types.DefaultGenesis()
	g.Authority = r.Sender
	g.Params.UnanimousRequiredForRevert = false
	g.Agents = []*types.AgentInfo{{AgentId: "jailed-agent", SignerAddress: r.Recipient, VotingPower: 0, Active: false}}
	f.keeper.InitGenesis(f.ctx, g)
	f.keeper = f.keeper.WithBankKeeper(&mockBankKeeper{})
	server := keeper.NewMsgServerImpl(f.keeper)
	f.keeper.SetOriginalTransfer(f.ctx, receipt(r))
	f.keeper.SetRiskReport(f.ctx, r)
	setSnapshotCase(t, f, &types.SanctionCase{CaseId: "settled", TxHash: r.TxHash, TargetAddress: r.Recipient, Status: types.CaseStatus_CASE_STATUS_APPROVED, RequestedAction: types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER, ReportIds: []string{r.ReportId}})
	_, err := server.ExecuteSanction(f.ctx, &types.MsgExecuteSanction{Executor: r.Sender, CaseId: "settled"})
	require.NoError(t, err)
	_, err = server.ReviewSanction(f.ctx, &types.MsgReviewSanction{Authority: r.Sender, CaseId: "settled", Reason: "accepted", EvidenceHash: bytes.Repeat([]byte{1}, 32)})
	require.NoError(t, err)
	_, err = server.RevokeSanction(f.ctx.WithBlockHeight(11), &types.MsgRevokeSanction{Authority: r.Sender, CaseId: "settled", Reason: "review accepted"})
	require.NoError(t, err)
	exported := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, types.ValidateGenesis(exported))
	var restored = &types.GenesisState{}
	require.NoError(t, types.ModuleCdc.UnmarshalJSON(types.ModuleCdc.MustMarshalJSON(exported), restored))
	other := newFixture(t)
	other.keeper.InitGenesis(other.ctx, restored)
	require.Equal(t, r.Sender, other.keeper.GetAuthority(other.ctx))
	require.True(t, proto.Equal(exported, other.keeper.ExportGenesis(other.ctx)))
	for _, mutate := range []func(*types.GenesisState){
		func(g *types.GenesisState) { g.Authority = "invalid" },
		func(g *types.GenesisState) {
			g.OriginalTransfers = append(g.OriginalTransfers, proto.Clone(g.OriginalTransfers[0]).(*types.OriginalTransfer))
		},
		func(g *types.GenesisState) {
			g.Settlements = append(g.Settlements, proto.Clone(g.Settlements[0]).(*types.SettlementRecord))
		},
		func(g *types.GenesisState) { g.Settlements[0].Transfer.Amount = "11" },
		func(g *types.GenesisState) { g.Settlements = nil },
		func(g *types.GenesisState) { g.Reviews[0].EvidenceHash = nil },
	} {
		bad := proto.Clone(exported).(*types.GenesisState)
		mutate(bad)
		require.Error(t, types.ValidateGenesis(bad))
	}
}
