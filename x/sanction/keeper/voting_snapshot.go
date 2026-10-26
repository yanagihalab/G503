package keeper

import (
	"fmt"
	"math/big"
	"sort"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/yanagihalab/G503/x/sanction/types"
	"google.golang.org/protobuf/proto"
)

func (k Keeper) SnapshotCaseVoters(ctx sdk.Context, c *types.SanctionCase) error {
	if c.VotingSnapshotVersion != 0 || len(c.Voters) != 0 {
		return fmt.Errorf("case snapshot is already set")
	}
	for _, a := range k.GetAllAgents(ctx) {
		if a.Active && a.VotingPower > 0 && len(a.PublicKey) > 0 {
			c.Voters = append(c.Voters, proto.Clone(a).(*types.AgentInfo))
		}
	}
	if len(c.Voters) == 0 {
		return fmt.Errorf("case requires a nonempty voter snapshot")
	}
	sort.Slice(c.Voters, func(i, j int) bool { return c.Voters[i].AgentId < c.Voters[j].AgentId })
	params := k.GetParams(ctx)
	c.QuorumThreshold = params.QuorumThreshold
	c.UnanimousRequiredForRevert = params.UnanimousRequiredForRevert
	c.VotingSnapshotVersion = 1
	return types.ValidateCaseSnapshot(c)
}

func caseVoter(c *types.SanctionCase, agentID string) (*types.AgentInfo, bool) {
	if c.VotingSnapshotVersion != 1 {
		return nil, false
	}
	for _, a := range c.Voters {
		if a != nil && a.AgentId == agentID {
			return a, true
		}
	}
	return nil, false
}

type snapshotTally struct {
	total   big.Int
	approve big.Int
	reject  big.Int
}

func tallySnapshotVotes(voters []*types.AgentInfo, votes []*types.SanctionVote) snapshotTally {
	// Bounded scans avoid map overhead for small committees without quadratic growth.
	var options map[string]types.VoteOption
	if len(votes) > 8 {
		options = make(map[string]types.VoteOption, len(votes))
		for _, vote := range votes {
			if vote == nil {
				continue
			}
			// Preserve first-match semantics; never iterate the map to tally consensus state.
			if _, found := options[vote.AgentId]; !found {
				options[vote.AgentId] = vote.Option
			}
		}
	}
	var tally snapshotTally
	var power big.Int
	for _, voter := range voters {
		if voter == nil || !voter.Active {
			continue
		}
		power.SetUint64(voter.VotingPower)
		tally.total.Add(&tally.total, &power)
		option := types.VoteOption_VOTE_OPTION_UNSPECIFIED
		if options != nil {
			option = options[voter.AgentId]
		} else {
			for _, vote := range votes {
				if vote != nil && vote.AgentId == voter.AgentId {
					option = vote.Option
					break
				}
			}
		}
		switch option {
		case types.VoteOption_VOTE_OPTION_APPROVE:
			tally.approve.Add(&tally.approve, &power)
		case types.VoteOption_VOTE_OPTION_REJECT:
			tally.reject.Add(&tally.reject, &power)
		}
	}
	return tally
}
