package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/x/sanction/keeper"
	"github.com/yanagihalab/G503/x/sanction/types"
)

func TestRegistrySyncKeepsDuplicateOrderingAndAgentIDCollisions(t *testing.T) {
	f := newFixture(t)
	f.keeper.SetAgent(f.ctx, &types.AgentInfo{AgentId: "validator:collision", SignerAddress: "existing-signer", Active: true, VotingPower: 7})
	states := []keeper.ValidatorAgentState{
		{ValidatorOperatorAddress: "new", SignerAddress: "new-signer", PublicKey: []byte{1}, Active: true, VotingPower: 3, MetadataURI: "first"},
		{ValidatorOperatorAddress: "new", SignerAddress: "new-signer", PublicKey: []byte{1}, Active: true, VotingPower: 9, MetadataURI: "second"},
		{ValidatorOperatorAddress: "collision", SignerAddress: "other-signer", PublicKey: []byte{1}, Active: true, VotingPower: 2},
	}
	updated, deactivated := f.keeper.SyncAgentRegistryWithValidatorSet(f.ctx, states)
	require.Equal(t, 1, updated)
	require.Zero(t, deactivated)
	created, found := f.keeper.GetAgentByValidatorOperator(f.ctx, "new")
	require.True(t, found)
	require.EqualValues(t, 3, created.VotingPower)
	require.Equal(t, "first", created.MetadataUri)
	collision, found := f.keeper.GetAgent(f.ctx, "validator:collision")
	require.True(t, found)
	require.Equal(t, "existing-signer", collision.SignerAddress)
	require.EqualValues(t, 7, collision.VotingPower)
	// Existing members use the last supplied state on subsequent synchronization.
	f.keeper.SyncAgentRegistryWithValidatorSet(f.ctx, states)
	created, _ = f.keeper.GetAgentByValidatorOperator(f.ctx, "new")
	require.EqualValues(t, 9, created.VotingPower)
	require.Equal(t, "first", created.MetadataUri)
	require.Len(t, f.keeper.GetAllAgents(f.ctx), 2)
}
