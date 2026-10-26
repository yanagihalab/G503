package keeper_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/x/sanction/keeper"
	st "github.com/yanagihalab/G503/x/sanction/types"
	"google.golang.org/protobuf/proto"
)

func setSnapshotCase(t *testing.T, f fixture, c *st.SanctionCase) {
	t.Helper()
	if len(f.keeper.GetAllAgents(f.ctx)) == 0 {
		registerAgent(t, f, "fixture-voter")
	}
	// Imported fixture cases supply the historical snapshot explicitly.
	if err := f.keeper.SnapshotCaseVoters(f.ctx, c); err != nil {
		registerAgent(t, f, "fixture-voter")
		require.NoError(t, f.keeper.SnapshotCaseVoters(f.ctx, c))
	}
	f.keeper.SetSanctionCase(f.ctx, c)
}

func TestCaseSnapshotSurvivesMembershipPowerKeysAndParamsChanges(t *testing.T) {
	f := newFixture(t)
	bank := &mockBankKeeper{}
	f.keeper = f.keeper.WithBankKeeper(bank)
	f.msgServer = keeper.NewMsgServerImpl(f.keeper)
	agents := []testAgent{}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		agents = append(agents, registerAgent(t, f, id))
	}
	r := safetyReport()
	f.keeper.SetOriginalTransfer(f.ctx, receipt(r))
	f.keeper.SetRiskReport(f.ctx, r)
	_, err := f.msgServer.OpenSanctionCase(f.ctx, &st.MsgOpenSanctionCase{Submitter: r.Sender, CaseId: "fixed", TxHash: r.TxHash, TargetAddress: r.Recipient, TargetType: st.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS, RequestedAction: st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER, ReportIds: []string{r.ReportId}})
	require.NoError(t, err)
	before, _ := f.keeper.GetSanctionCase(f.ctx, "fixed")
	for _, a := range agents[:4] {
		voteAction(t, f, a, "fixed", st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER)
	}
	// Removal, jail, key replacement and power changes must not reduce the frozen denominator.
	for i, a := range agents {
		a.info.Active = i < 3
		a.info.VotingPower = 99
		a.info.PublicKey = bytes.Repeat([]byte{2}, 33)
		f.keeper.SetAgent(f.ctx, a.info)
	}
	newAgent := registerAgent(t, f, "new-validator")
	p := f.keeper.GetParams(f.ctx)
	p.QuorumThreshold = 1
	p.UnanimousRequiredForRevert = false
	f.keeper.SetParams(f.ctx, p)
	_, err = f.msgServer.ExecuteSanction(f.ctx, &st.MsgExecuteSanction{Executor: r.Sender, CaseId: "fixed"})
	require.ErrorIs(t, err, st.ErrQuorumNotReached)
	_, err = f.msgServer.SubmitSanctionVote(f.ctx, voteMsg(t, f, newAgent, "fixed", st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER))
	require.ErrorIs(t, err, st.ErrAgentNotFound)
	voteAction(t, f, agents[4], "fixed", st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER)
	_, err = f.msgServer.ExecuteSanction(f.ctx, &st.MsgExecuteSanction{Executor: r.Sender, CaseId: "fixed"})
	require.NoError(t, err)
	after, _ := f.keeper.GetSanctionCase(f.ctx, "fixed")
	require.Len(t, after.Voters, 5)
	require.Equal(t, before.QuorumThreshold, after.QuorumThreshold)
	require.True(t, after.UnanimousRequiredForRevert)
	for i := range before.Voters {
		require.True(t, proto.Equal(before.Voters[i], after.Voters[i]))
	}
	require.Equal(t, 1, bank.sendCoinsCalls)
}
