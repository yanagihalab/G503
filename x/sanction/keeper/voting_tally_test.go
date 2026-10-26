package keeper

import (
	"fmt"
	"math/big"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/x/sanction/types"
)

// The original nested loop remains as an independent reference, not a production fallback.
func quadraticSnapshotTally(voters []*types.AgentInfo, votes []*types.SanctionVote) snapshotTally {
	var tally snapshotTally
	for _, voter := range voters {
		if voter == nil || !voter.Active {
			continue
		}
		power := new(big.Int).SetUint64(voter.VotingPower)
		tally.total.Add(&tally.total, power)
		for _, vote := range votes {
			if vote == nil || vote.AgentId != voter.AgentId {
				continue
			}
			switch vote.Option {
			case types.VoteOption_VOTE_OPTION_APPROVE:
				tally.approve.Add(&tally.approve, power)
			case types.VoteOption_VOTE_OPTION_REJECT:
				tally.reject.Add(&tally.reject, power)
			}
			break
		}
	}
	return tally
}

func TestIndexedSnapshotTallyMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(503))
	for sample := 0; sample < 500; sample++ {
		voters := make([]*types.AgentInfo, rng.Intn(50))
		votes := make([]*types.SanctionVote, rng.Intn(100))
		for i := range voters {
			if rng.Intn(8) == 0 {
				continue
			}
			voters[i] = &types.AgentInfo{AgentId: fmt.Sprint(i), VotingPower: rng.Uint64(), Active: rng.Intn(4) != 0}
		}
		for i := range votes {
			if rng.Intn(8) == 0 {
				continue
			}
			votes[i] = &types.SanctionVote{AgentId: fmt.Sprint(rng.Intn(60)), Option: types.VoteOption(rng.Intn(5))}
		}
		want, got := quadraticSnapshotTally(voters, votes), tallySnapshotVotes(voters, votes)
		require.Zero(t, want.total.Cmp(&got.total), "sample %d total", sample)
		require.Zero(t, want.approve.Cmp(&got.approve), "sample %d approvals", sample)
		require.Zero(t, want.reject.Cmp(&got.reject), "sample %d rejections", sample)
	}
}

func BenchmarkSnapshotTally(b *testing.B) {
	for _, size := range []int{5, 100, 1000} {
		voters := make([]*types.AgentInfo, size)
		votes := make([]*types.SanctionVote, size)
		for i := range voters {
			voters[i] = &types.AgentInfo{AgentId: fmt.Sprint(i), Active: true, VotingPower: 1}
			votes[size-i-1] = &types.SanctionVote{AgentId: voters[i].AgentId, Option: types.VoteOption_VOTE_OPTION_APPROVE}
		}
		for _, implementation := range []struct {
			name  string
			tally func([]*types.AgentInfo, []*types.SanctionVote) snapshotTally
		}{{"quadratic", quadraticSnapshotTally}, {"indexed", tallySnapshotVotes}} {
			b.Run(fmt.Sprintf("%d/%s", size, implementation.name), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					implementation.tally(voters, votes)
				}
			})
		}
	}
}
