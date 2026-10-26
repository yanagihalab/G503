package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/yanagihalab/G503/x/sanction/types"
)

func (k Keeper) InitGenesis(ctx sdk.Context, genesis *types.GenesisState) {
	if genesis == nil {
		genesis = types.DefaultGenesis()
	}
	if genesis.Params != nil {
		k.SetParams(ctx, genesis.Params)
	}
	if genesis.Authority != "" {
		k.Store(ctx).Set(types.AuthorityKey, []byte(genesis.Authority))
	}
	for _, lease := range genesis.AgentLeases {
		k.SetAgentLease(ctx, lease)
	}
	for _, transfer := range genesis.OriginalTransfers {
		k.SetOriginalTransfer(ctx, transfer)
	}
	for _, record := range genesis.Settlements {
		k.SetSettlement(ctx, record)
	}
	for _, review := range genesis.Reviews {
		k.SetReview(ctx, review)
	}
	for _, agent := range genesis.Agents {
		if agent != nil {
			k.SetAgent(ctx, agent)
		}
	}
	for _, report := range genesis.RiskReports {
		if report != nil {
			k.SetRiskReport(ctx, report)
		}
	}
	for _, sanctionCase := range genesis.SanctionCases {
		if sanctionCase != nil {
			k.SetSanctionCase(ctx, sanctionCase)
		}
	}
	for _, vote := range genesis.SanctionVotes {
		if vote != nil {
			k.SetSanctionVote(ctx, vote)
		}
	}
	for _, sanction := range genesis.ActiveSanctions {
		if sanction != nil {
			k.SetActiveTxSanction(ctx, sanction)
		}
	}
	for _, freeze := range genesis.FrozenAddresses {
		if freeze != nil {
			k.SetFreezeRecord(ctx, freeze)
		}
	}
	for _, record := range genesis.ExecutionRecords {
		if record != nil {
			k.SetExecutionRecord(ctx, record)
		}
	}
}

func (k Keeper) ExportGenesis(ctx sdk.Context) *types.GenesisState {
	params := k.GetParams(ctx)
	return &types.GenesisState{
		AgentLeases:       k.GetAllAgentLeases(ctx),
		Authority:         k.GetAuthority(ctx),
		OriginalTransfers: k.GetOriginalTransfers(ctx, nil),
		Settlements:       k.GetSettlements(ctx, nil),
		Reviews:           k.GetAllReviews(ctx),
		Params:            params,
		Agents:            k.GetAllAgents(ctx),
		RiskReports:       k.GetAllRiskReports(ctx),
		SanctionCases:     k.GetAllSanctionCases(ctx),
		SanctionVotes:     k.getAllSanctionVotes(ctx),
		ActiveSanctions:   k.GetAllActiveSanctions(ctx),
		FrozenAddresses:   k.GetAllFreezeRecords(ctx),
		ExecutionRecords:  k.GetAllExecutionRecords(ctx),
	}
}

func (k Keeper) getAllSanctionVotes(ctx sdk.Context) []*types.SanctionVote {
	votes := []*types.SanctionVote{}
	for _, sanctionCase := range k.GetAllSanctionCases(ctx) {
		if sanctionCase == nil {
			continue
		}
		votes = append(votes, k.GetSanctionVotes(ctx, sanctionCase.CaseId)...)
	}
	return votes
}
