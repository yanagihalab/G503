package keeper

import (
	"strconv"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/protobuf/proto"

	"github.com/yanagihalab/G503/x/sanction/types"
)

type ValidatorAgentState struct {
	ValidatorOperatorAddress string
	SignerAddress            string
	PublicKey                []byte
	VotingPower              uint64
	Active                   bool
	MetadataURI              string
}

func (k Keeper) SyncAgentRegistryWithValidatorSet(ctx sdk.Context, validatorStates []ValidatorAgentState) (updated int, deactivated int) {
	byOperator := make(map[string]ValidatorAgentState, len(validatorStates))
	for _, state := range validatorStates {
		if state.ValidatorOperatorAddress == "" {
			continue
		}
		byOperator[state.ValidatorOperatorAddress] = state
	}
	agents := k.GetAllAgents(ctx)
	knownOperators := make(map[string]bool, len(agents))
	knownAgentIDs := make(map[string]bool, len(agents))
	for _, agent := range agents {
		if agent == nil {
			continue
		}
		knownAgentIDs[agent.AgentId] = true
		if agent.ValidatorOperatorAddress == "" {
			continue
		}
		knownOperators[agent.ValidatorOperatorAddress] = true
		state, found := byOperator[agent.ValidatorOperatorAddress]
		next := proto.Clone(agent).(*types.AgentInfo)
		if found && next.SignerAddress == state.SignerAddress && len(next.PublicKey) == 0 {
			next.PublicKey = state.PublicKey
		}
		if !found || !state.Active || state.VotingPower == 0 || len(next.PublicKey) == 0 {
			if next.Active {
				deactivated++
			}
			next.Active = false
			next.VotingPower = 0
			k.SetAgent(ctx, next)
			continue
		}
		if next.VotingPower != state.VotingPower || !next.Active {
			updated++
		}
		next.Active = true
		next.VotingPower = state.VotingPower
		k.SetAgent(ctx, next)
	}
	for _, state := range validatorStates {
		if state.ValidatorOperatorAddress == "" || state.SignerAddress == "" {
			continue
		}
		if !state.Active || state.VotingPower == 0 || len(state.PublicKey) == 0 {
			continue
		}
		if knownOperators[state.ValidatorOperatorAddress] {
			continue
		}
		agentID := "validator:" + state.ValidatorOperatorAddress
		if knownAgentIDs[agentID] {
			continue
		}
		metadataURI := state.MetadataURI
		if metadataURI == "" {
			metadataURI = "staking-sync"
		}
		k.SetAgent(ctx, &types.AgentInfo{
			AgentId:                  agentID,
			ValidatorOperatorAddress: state.ValidatorOperatorAddress,
			SignerAddress:            state.SignerAddress,
			PublicKey:                state.PublicKey,
			VotingPower:              state.VotingPower,
			Active:                   true,
			MetadataUri:              metadataURI,
		})
		knownOperators[state.ValidatorOperatorAddress] = true
		knownAgentIDs[agentID] = true
		updated++
	}
	if updated > 0 || deactivated > 0 {
		ctx.EventManager().EmitEvent(sdk.NewEvent(
			"agent_validator_set_synced",
			sdk.NewAttribute("updated", uint64ToString(updated)),
			sdk.NewAttribute("deactivated", uint64ToString(deactivated)),
		))
	}
	return updated, deactivated
}

func (k Keeper) GetAgentByValidatorOperator(ctx sdk.Context, operatorAddress string) (*types.AgentInfo, bool) {
	for _, agent := range k.GetAllAgents(ctx) {
		if agent != nil && agent.ValidatorOperatorAddress == operatorAddress {
			return agent, true
		}
	}
	return &types.AgentInfo{}, false
}

func uint64ToString(value int) string {
	return strconv.FormatUint(uint64(value), 10)
}
