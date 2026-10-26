package keeper_test

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/x/sanction/keeper"
	"github.com/yanagihalab/G503/x/sanction/types"
	"google.golang.org/protobuf/proto"
)

func TestPointerKeeperAPIsDoNotShareCallerOrStoredMessages(t *testing.T) {
	f := newFixture(t)
	agent := &types.AgentInfo{AgentId: "agent", PublicKey: []byte{1, 2}, VotingPower: 5}
	f.keeper.SetAgent(f.ctx, agent)
	agent.PublicKey[0] = 9
	agent.VotingPower = 99
	first, found := f.keeper.GetAgent(f.ctx, "agent")
	require.True(t, found)
	require.EqualValues(t, 5, first.VotingPower)
	require.Equal(t, []byte{1, 2}, first.PublicKey)
	first.PublicKey[0] = 7
	second, found := f.keeper.GetAgent(f.ctx, "agent")
	require.True(t, found)
	require.NotSame(t, first, second)
	require.Equal(t, []byte{1, 2}, second.PublicKey)
	params := f.keeper.GetParams(f.ctx)
	params.AcceptedRiskSources[0] = "changed"
	require.NotEqual(t, "changed", f.keeper.GetParams(f.ctx).AcceptedRiskSources[0])
	defaults := types.DefaultParams()
	defaults.HighRiskCategories[0] = "changed"
	require.NotEqual(t, "changed", types.DefaultParams().HighRiskCategories[0])
	require.Error(t, types.ValidateParams(nil))
	require.Error(t, types.ValidateAgent(nil))
}

func TestPointerHashAPIsPreserveWireBytesAndInputs(t *testing.T) {
	r := &types.RiskReport{ReportId: "report", EvidenceHash: []byte{1, 2}, SubmitterSignature: []byte{3, 4}}
	rBefore := proto.Clone(r)
	unsigned := proto.Clone(r).(*types.RiskReport)
	unsigned.SubmitterSignature = nil
	want := keeper.RiskReportSignBytes("test", unsigned)
	require.Equal(t, want, keeper.RiskReportSignBytes("test", r))
	require.True(t, proto.Equal(rBefore, r))
	c := &types.SanctionCase{CaseId: "case", DecisionHash: []byte{7}, Voters: []*types.AgentInfo{{AgentId: "agent", PublicKey: []byte{8}}}}
	cBefore := proto.Clone(c)
	unsignedCase := proto.Clone(c).(*types.SanctionCase)
	unsignedCase.DecisionHash = nil
	expected := sha256.Sum256(types.ModuleCdc.MustMarshal(unsignedCase))
	require.Equal(t, expected[:], keeper.DecisionHash(c))
	require.True(t, proto.Equal(cBefore, c))
}
