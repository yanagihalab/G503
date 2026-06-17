package keeper

import (
	prefix "cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/yanagihalab/G503/x/sanction/types"
)

func (k Keeper) GetParams(ctx sdk.Context) types.Params {
	store := k.Store(ctx)
	bz := store.Get(types.ParamsKey)
	if bz == nil {
		return types.DefaultParams()
	}

	var params types.Params
	types.ModuleCdc.MustUnmarshal(bz, &params)
	return params
}

func (k Keeper) SetParams(ctx sdk.Context, params types.Params) {
	k.Store(ctx).Set(types.ParamsKey, types.ModuleCdc.MustMarshal(&params))
}

func (k Keeper) SetAgent(ctx sdk.Context, agent types.AgentInfo) {
	store := k.Store(ctx)
	bz := types.ModuleCdc.MustMarshal(&agent)
	store.Set(agentKey(agent.AgentId), bz)
	store.Set(agentBySignerKey(agent.SignerAddress), []byte(agent.AgentId))
}

func (k Keeper) GetAgent(ctx sdk.Context, agentID string) (types.AgentInfo, bool) {
	bz := k.Store(ctx).Get(agentKey(agentID))
	if bz == nil {
		return types.AgentInfo{}, false
	}

	var agent types.AgentInfo
	types.ModuleCdc.MustUnmarshal(bz, &agent)
	return agent, true
}

func (k Keeper) GetAgentBySigner(ctx sdk.Context, signer string) (types.AgentInfo, bool) {
	agentIDBz := k.Store(ctx).Get(agentBySignerKey(signer))
	if agentIDBz == nil {
		return types.AgentInfo{}, false
	}
	return k.GetAgent(ctx, string(agentIDBz))
}

func (k Keeper) GetAllAgents(ctx sdk.Context) []*types.AgentInfo {
	store := prefix.NewStore(k.Store(ctx), types.AgentKeyPrefix)
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})
	defer iterator.Close()

	agents := []*types.AgentInfo{}
	for ; iterator.Valid(); iterator.Next() {
		var agent types.AgentInfo
		types.ModuleCdc.MustUnmarshal(iterator.Value(), &agent)
		agents = append(agents, &agent)
	}
	return agents
}

func (k Keeper) SetRiskReport(ctx sdk.Context, report types.RiskReport) {
	store := k.Store(ctx)
	bz := types.ModuleCdc.MustMarshal(&report)
	store.Set(riskReportKey(report.ReportId), bz)
	store.Set(riskReportByTxKey(report.TxHash, report.ReportId), []byte(report.ReportId))
	if report.Recipient != "" {
		store.Set(riskReportByAddressKey(report.Recipient, report.ReportId), []byte(report.ReportId))
	}
}

func (k Keeper) GetRiskReport(ctx sdk.Context, reportID string) (types.RiskReport, bool) {
	bz := k.Store(ctx).Get(riskReportKey(reportID))
	if bz == nil {
		return types.RiskReport{}, false
	}

	var report types.RiskReport
	types.ModuleCdc.MustUnmarshal(bz, &report)
	return report, true
}

func (k Keeper) GetAllRiskReports(ctx sdk.Context) []*types.RiskReport {
	store := prefix.NewStore(k.Store(ctx), types.RiskReportKeyPrefix)
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})
	defer iterator.Close()

	reports := []*types.RiskReport{}
	for ; iterator.Valid(); iterator.Next() {
		var report types.RiskReport
		types.ModuleCdc.MustUnmarshal(iterator.Value(), &report)
		reports = append(reports, &report)
	}
	return reports
}

func (k Keeper) GetRiskReportsByTx(ctx sdk.Context, txHash []byte) []*types.RiskReport {
	store := prefix.NewStore(k.Store(ctx), txPrefix(types.RiskReportByTxKeyPrefix, txHash))
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})
	defer iterator.Close()

	reports := []*types.RiskReport{}
	for ; iterator.Valid(); iterator.Next() {
		reportID, ok := splitIndexedValue(iterator.Value())
		if !ok {
			continue
		}
		report, found := k.GetRiskReport(ctx, reportID)
		if found {
			reports = append(reports, &report)
		}
	}
	return reports
}

func (k Keeper) SetSanctionCase(ctx sdk.Context, sanctionCase types.SanctionCase) {
	store := k.Store(ctx)
	bz := types.ModuleCdc.MustMarshal(&sanctionCase)
	store.Set(sanctionCaseKey(sanctionCase.CaseId), bz)
	if len(sanctionCase.TxHash) > 0 {
		store.Set(caseByTxKey(sanctionCase.TxHash, sanctionCase.CaseId), []byte(sanctionCase.CaseId))
	}
	if sanctionCase.TargetAddress != "" {
		store.Set(caseByAddressKey(sanctionCase.TargetAddress, sanctionCase.CaseId), []byte(sanctionCase.CaseId))
	}
}

func (k Keeper) GetSanctionCase(ctx sdk.Context, caseID string) (types.SanctionCase, bool) {
	bz := k.Store(ctx).Get(sanctionCaseKey(caseID))
	if bz == nil {
		return types.SanctionCase{}, false
	}

	var sanctionCase types.SanctionCase
	types.ModuleCdc.MustUnmarshal(bz, &sanctionCase)
	return sanctionCase, true
}

func (k Keeper) GetAllSanctionCases(ctx sdk.Context) []*types.SanctionCase {
	store := prefix.NewStore(k.Store(ctx), types.SanctionCaseKeyPrefix)
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})
	defer iterator.Close()

	cases := []*types.SanctionCase{}
	for ; iterator.Valid(); iterator.Next() {
		var sanctionCase types.SanctionCase
		types.ModuleCdc.MustUnmarshal(iterator.Value(), &sanctionCase)
		cases = append(cases, &sanctionCase)
	}
	return cases
}

func (k Keeper) GetSanctionCasesByTx(ctx sdk.Context, txHash []byte) []*types.SanctionCase {
	store := prefix.NewStore(k.Store(ctx), txPrefix(types.CaseByTxKeyPrefix, txHash))
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})
	defer iterator.Close()

	cases := []*types.SanctionCase{}
	for ; iterator.Valid(); iterator.Next() {
		caseID, ok := splitIndexedValue(iterator.Value())
		if !ok {
			continue
		}
		sanctionCase, found := k.GetSanctionCase(ctx, caseID)
		if found {
			cases = append(cases, &sanctionCase)
		}
	}
	return cases
}

func (k Keeper) SetSanctionVote(ctx sdk.Context, vote types.SanctionVote) {
	k.Store(ctx).Set(sanctionVoteKey(vote.CaseId, vote.AgentId), types.ModuleCdc.MustMarshal(&vote))
}

func (k Keeper) GetSanctionVote(ctx sdk.Context, caseID string, agentID string) (types.SanctionVote, bool) {
	bz := k.Store(ctx).Get(sanctionVoteKey(caseID, agentID))
	if bz == nil {
		return types.SanctionVote{}, false
	}

	var vote types.SanctionVote
	types.ModuleCdc.MustUnmarshal(bz, &vote)
	return vote, true
}

func (k Keeper) GetSanctionVotes(ctx sdk.Context, caseID string) []*types.SanctionVote {
	p := append([]byte{}, types.SanctionVoteKeyPrefix...)
	p = append(p, []byte(caseID)...)
	p = append(p, 0x00)
	store := prefix.NewStore(k.Store(ctx), p)
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})
	defer iterator.Close()

	votes := []*types.SanctionVote{}
	for ; iterator.Valid(); iterator.Next() {
		var vote types.SanctionVote
		types.ModuleCdc.MustUnmarshal(iterator.Value(), &vote)
		votes = append(votes, &vote)
	}
	return votes
}

func (k Keeper) SetActiveTxSanction(ctx sdk.Context, sanction types.ActiveSanction) {
	k.Store(ctx).Set(activeTxSanctionKey(sanction.TxHash), types.ModuleCdc.MustMarshal(&sanction))
}

func (k Keeper) GetActiveTxSanction(ctx sdk.Context, txHash []byte) (types.ActiveSanction, bool) {
	bz := k.Store(ctx).Get(activeTxSanctionKey(txHash))
	if bz == nil {
		return types.ActiveSanction{}, false
	}

	var sanction types.ActiveSanction
	types.ModuleCdc.MustUnmarshal(bz, &sanction)
	return sanction, true
}

func (k Keeper) DeleteActiveTxSanction(ctx sdk.Context, txHash []byte) {
	k.Store(ctx).Delete(activeTxSanctionKey(txHash))
}

func (k Keeper) SetFreezeRecord(ctx sdk.Context, freeze types.FreezeRecord) {
	k.Store(ctx).Set(frozenAddressKey(freeze.Address), types.ModuleCdc.MustMarshal(&freeze))
}

func (k Keeper) GetFreezeRecord(ctx sdk.Context, address string) (types.FreezeRecord, bool) {
	bz := k.Store(ctx).Get(frozenAddressKey(address))
	if bz == nil {
		return types.FreezeRecord{}, false
	}

	var freeze types.FreezeRecord
	types.ModuleCdc.MustUnmarshal(bz, &freeze)
	return freeze, true
}

func (k Keeper) DeleteFreezeRecord(ctx sdk.Context, address string) {
	k.Store(ctx).Delete(frozenAddressKey(address))
}

func (k Keeper) SetExecutionRecord(ctx sdk.Context, record types.ExecutionRecord) {
	k.Store(ctx).Set(executionRecordKey(record.CaseId), types.ModuleCdc.MustMarshal(&record))
}

func (k Keeper) GetExecutionRecord(ctx sdk.Context, caseID string) (types.ExecutionRecord, bool) {
	bz := k.Store(ctx).Get(executionRecordKey(caseID))
	if bz == nil {
		return types.ExecutionRecord{}, false
	}

	var record types.ExecutionRecord
	types.ModuleCdc.MustUnmarshal(bz, &record)
	return record, true
}

func (k Keeper) GetAllExecutionRecords(ctx sdk.Context) []*types.ExecutionRecord {
	store := prefix.NewStore(k.Store(ctx), types.ExecutionRecordKeyPrefix)
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})
	defer iterator.Close()

	records := []*types.ExecutionRecord{}
	for ; iterator.Valid(); iterator.Next() {
		var record types.ExecutionRecord
		types.ModuleCdc.MustUnmarshal(iterator.Value(), &record)
		records = append(records, &record)
	}
	return records
}

func (k Keeper) GetAllActiveSanctions(ctx sdk.Context) []*types.ActiveSanction {
	store := prefix.NewStore(k.Store(ctx), types.ActiveTxSanctionKeyPrefix)
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})
	defer iterator.Close()

	sanctions := []*types.ActiveSanction{}
	for ; iterator.Valid(); iterator.Next() {
		var sanction types.ActiveSanction
		types.ModuleCdc.MustUnmarshal(iterator.Value(), &sanction)
		sanctions = append(sanctions, &sanction)
	}
	return sanctions
}

func (k Keeper) GetAllFreezeRecords(ctx sdk.Context) []*types.FreezeRecord {
	store := prefix.NewStore(k.Store(ctx), types.FrozenAddressKeyPrefix)
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})
	defer iterator.Close()

	records := []*types.FreezeRecord{}
	for ; iterator.Valid(); iterator.Next() {
		var record types.FreezeRecord
		types.ModuleCdc.MustUnmarshal(iterator.Value(), &record)
		records = append(records, &record)
	}
	return records
}
