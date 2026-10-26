package keeper_test

import (
	"fmt"
	"testing"

	"github.com/yanagihalab/G503/x/sanction/keeper"
	"github.com/yanagihalab/G503/x/sanction/types"
)

func BenchmarkSyncAgentRegistry(b *testing.B) {
	for _, size := range []int{5, 100, 1000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			f := newFixture(b)
			states := make([]keeper.ValidatorAgentState, size)
			for i := range states {
				states[i] = keeper.ValidatorAgentState{
					ValidatorOperatorAddress: fmt.Sprintf("operator-%04d", i),
					SignerAddress:            fmt.Sprintf("signer-%04d", i),
					PublicKey:                make([]byte, 33),
					VotingPower:              1,
					Active:                   true,
				}
				f.keeper.SetAgent(f.ctx, &types.AgentInfo{
					AgentId:                  fmt.Sprintf("agent-%04d", i),
					ValidatorOperatorAddress: states[i].ValidatorOperatorAddress,
					SignerAddress:            states[i].SignerAddress,
					PublicKey:                states[i].PublicKey,
					VotingPower:              1,
					Active:                   true,
				})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				f.keeper.SyncAgentRegistryWithValidatorSet(f.ctx, states)
			}
		})
	}
}
