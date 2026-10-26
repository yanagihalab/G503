package keeper_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/x/sanction/keeper"
	st "github.com/yanagihalab/G503/x/sanction/types"
	"google.golang.org/protobuf/proto"
)

func TestLeaseEpochExclusiveExpiryAndGenesis(t *testing.T) {
	f := newFixture(t)
	a := registerAgent(t, f, "owner")
	claim := &st.MsgClaimAgentLease{SignerAddress: a.info.SignerAddress, HolderId: strings.Repeat("a", 64), TtlBlocks: 6}
	one, err := f.msgServer.ClaimAgentLease(f.ctx, claim)
	require.NoError(t, err)
	require.EqualValues(t, 1, one.Lease.Epoch)
	race := proto.Clone(claim).(*st.MsgClaimAgentLease)
	race.HolderId = strings.Repeat("c", 64)
	_, err = f.msgServer.ClaimAgentLease(f.ctx, race)
	require.ErrorContains(t, err, "epoch changed", "simultaneous startup claims use compare-and-swap at the same height")
	other := proto.Clone(claim).(*st.MsgClaimAgentLease)
	other.ExpectedEpoch = 1
	other.HolderId = strings.Repeat("b", 64)
	_, err = f.msgServer.ClaimAgentLease(f.ctx, other)
	require.Error(t, err)
	claim.ExpectedEpoch = 1
	claim.Renew = true
	_, err = f.msgServer.ClaimAgentLease(f.ctx.WithBlockHeight(16), claim)
	require.Error(t, err, "expired process must never renew")
	two, err := f.msgServer.ClaimAgentLease(f.ctx.WithBlockHeight(16), other)
	require.NoError(t, err)
	require.EqualValues(t, 2, two.Lease.Epoch)
	_, err = f.msgServer.ClaimAgentLease(f.ctx.WithBlockHeight(17), claim)
	require.Error(t, err, "old host cannot regain ownership")
	g := f.keeper.ExportGenesis(f.ctx)
	g.Authority = a.info.SignerAddress
	require.NoError(t, st.ValidateGenesis(g))
	otherFixture := newFixture(t)
	otherFixture.keeper.InitGenesis(otherFixture.ctx, g)
	restored, found := otherFixture.keeper.GetAgentLease(otherFixture.ctx, a.info.SignerAddress)
	require.True(t, found)
	require.True(t, proto.Equal(two.Lease, restored))
	require.NotEqual(t, keeper.LeaseMemo(one.Lease), keeper.LeaseMemo(two.Lease))
}
