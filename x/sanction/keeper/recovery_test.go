package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/x/sanction/keeper"
	st "github.com/yanagihalab/G503/x/sanction/types"
)

func TestExpiredApprovedCaseNeedsFreshFiveVotesAndCannotDoubleExecute(t *testing.T) {
	f := newFixture(t)
	bank := &mockBankKeeper{}
	f.keeper = f.keeper.WithBankKeeper(bank)
	f.msgServer = keeper.NewMsgServerImpl(f.keeper)
	r := safetyReport()
	r.ObservedHeight = 10
	r.ExpiryHeight = 14
	f.keeper.SetOriginalTransfer(f.ctx, receipt(r))
	f.keeper.SetRiskReport(f.ctx, r)
	open := func(id, previous, report string) *st.MsgOpenSanctionCase {
		return &st.MsgOpenSanctionCase{Submitter: r.Sender, CaseId: id, PreviousCaseId: previous, TxHash: r.TxHash, TargetAddress: r.Recipient, TargetType: st.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS, RequestedAction: st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER, ReportIds: []string{report}}
	}
	var agents []testAgent
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		agents = append(agents, registerAgent(t, f, id))
	}
	_, err := f.msgServer.OpenSanctionCase(f.ctx, open("old", "", r.ReportId))
	require.NoError(t, err)
	for _, a := range agents {
		voteAction(t, f, a, "old", st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER)
	}
	f.ctx = f.ctx.WithBlockHeight(15)
	_, err = f.msgServer.ExecuteSanction(f.ctx, &st.MsgExecuteSanction{Executor: r.Sender, CaseId: "old"})
	require.ErrorIs(t, err, st.ErrRiskReportExpired)
	r.ReportId = "fresh"
	r.ObservedHeight = 15
	r.ExpiryHeight = 45
	f.keeper.SetRiskReport(f.ctx, r)
	_, err = f.msgServer.OpenSanctionCase(f.ctx, open("new", "old", r.ReportId))
	require.NoError(t, err)
	old, _ := f.keeper.GetSanctionCase(f.ctx, "old")
	require.Equal(t, st.CaseStatus_CASE_STATUS_EXPIRED, old.Status)
	require.Len(t, f.keeper.GetSanctionVotes(f.ctx, "old"), 5)
	require.Empty(t, f.keeper.GetSanctionVotes(f.ctx, "new"))
	_, err = f.msgServer.ExecuteSanction(f.ctx, &st.MsgExecuteSanction{Executor: r.Sender, CaseId: "old"})
	require.Error(t, err)
	_, err = f.msgServer.OpenSanctionCase(f.ctx, open("fork", "old", r.ReportId))
	require.ErrorIs(t, err, st.ErrDuplicateCase)
	for i, a := range agents {
		voteAction(t, f, a, "new", st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER)
		if i == 3 {
			_, err = f.msgServer.ExecuteSanction(f.ctx, &st.MsgExecuteSanction{Executor: r.Sender, CaseId: "new"})
			require.ErrorIs(t, err, st.ErrQuorumNotReached)
		}
	}
	_, err = f.msgServer.ExecuteSanction(f.ctx, &st.MsgExecuteSanction{Executor: r.Sender, CaseId: "new"})
	require.NoError(t, err)
	require.Equal(t, 1, bank.sendCoinsCalls)
}

func TestReplacementCannotReviveFinalDecisionsOrReuseEvidence(t *testing.T) {
	for _, scenario := range []string{"valid-evidence", "rejected", "revoked", "executed", "same-observation", "different-transfer"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			r := safetyReport()
			r.ObservedHeight = 5
			r.ExpiryHeight = 8
			f.keeper.SetOriginalTransfer(f.ctx, receipt(r))
			f.keeper.SetRiskReport(f.ctx, r)
			c := &st.SanctionCase{CaseId: "old", TxHash: r.TxHash, TargetAddress: r.Recipient, TargetType: st.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS, RequestedAction: st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER, Status: st.CaseStatus_CASE_STATUS_PENDING, VotingDeadlineHeight: 20, ReportIds: []string{r.ReportId}}
			switch scenario {
			case "valid-evidence":
				r.ExpiryHeight = 40
				f.keeper.SetRiskReport(f.ctx, r)
			case "rejected":
				c.Status = st.CaseStatus_CASE_STATUS_REJECTED
			case "revoked":
				c.Status = st.CaseStatus_CASE_STATUS_REVOKED
			case "executed":
				c.Status = st.CaseStatus_CASE_STATUS_EXECUTED
				c.ExecutedHeight = 9
			}
			f.keeper.SetSanctionCase(f.ctx, c)
			r.ReportId = "fresh"
			r.ExpiryHeight = 40
			r.ObservedHeight = 10
			if scenario == "same-observation" {
				r.ObservedHeight = 5
			}
			if scenario == "different-transfer" {
				r.Sender = r.Recipient
			}
			f.keeper.SetRiskReport(f.ctx, r)
			_, err := f.msgServer.OpenSanctionCase(f.ctx, &st.MsgOpenSanctionCase{Submitter: r.Sender, CaseId: "new", PreviousCaseId: "old", TxHash: r.TxHash, TargetAddress: r.Recipient, TargetType: st.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS, RequestedAction: st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER, ReportIds: []string{r.ReportId}})
			require.Error(t, err)
			_, found := f.keeper.GetSanctionCase(f.ctx, "new")
			require.False(t, found)
		})
	}
}
