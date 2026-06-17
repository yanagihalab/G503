package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/yanagihalab/G503/x/sanction/types"
)

var _ types.QueryServer = Keeper{}

func (k Keeper) Params(goCtx context.Context, _ *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	params := k.GetParams(ctx)
	return &types.QueryParamsResponse{Params: &params}, nil
}

func (k Keeper) Agent(goCtx context.Context, req *types.QueryAgentRequest) (*types.QueryAgentResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	agent, found := k.GetAgent(ctx, req.AgentId)
	if !found {
		return nil, types.ErrAgentNotFound
	}
	return &types.QueryAgentResponse{Agent: &agent}, nil
}

func (k Keeper) Agents(goCtx context.Context, _ *types.QueryAgentsRequest) (*types.QueryAgentsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return &types.QueryAgentsResponse{Agents: k.GetAllAgents(ctx)}, nil
}

func (k Keeper) RiskReport(goCtx context.Context, req *types.QueryRiskReportRequest) (*types.QueryRiskReportResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	report, found := k.GetRiskReport(ctx, req.ReportId)
	if !found {
		return nil, types.ErrRiskReportNotFound
	}
	return &types.QueryRiskReportResponse{Report: &report}, nil
}

func (k Keeper) RiskReportsByTx(goCtx context.Context, req *types.QueryRiskReportsByTxRequest) (*types.QueryRiskReportsByTxResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	txHash, err := parseHexHash(req.TxHash)
	if err != nil {
		return nil, err
	}
	return &types.QueryRiskReportsByTxResponse{Reports: k.GetRiskReportsByTx(ctx, txHash)}, nil
}

func (k Keeper) SanctionCase(goCtx context.Context, req *types.QuerySanctionCaseRequest) (*types.QuerySanctionCaseResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	sanctionCase, found := k.GetSanctionCase(ctx, req.CaseId)
	if !found {
		return nil, types.ErrCaseNotFound
	}
	return &types.QuerySanctionCaseResponse{SanctionCase: &sanctionCase}, nil
}

func (k Keeper) SanctionCasesByTx(goCtx context.Context, req *types.QuerySanctionCasesByTxRequest) (*types.QuerySanctionCasesByTxResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	txHash, err := parseHexHash(req.TxHash)
	if err != nil {
		return nil, err
	}
	return &types.QuerySanctionCasesByTxResponse{SanctionCases: k.GetSanctionCasesByTx(ctx, txHash)}, nil
}

func (k Keeper) SanctionVotes(goCtx context.Context, req *types.QuerySanctionVotesRequest) (*types.QuerySanctionVotesResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return &types.QuerySanctionVotesResponse{Votes: k.GetSanctionVotes(ctx, req.CaseId)}, nil
}

func (k Keeper) ActiveTxSanction(goCtx context.Context, req *types.QueryActiveTxSanctionRequest) (*types.QueryActiveTxSanctionResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	txHash, err := parseHexHash(req.TxHash)
	if err != nil {
		return nil, err
	}
	sanction, found := k.GetActiveTxSanction(ctx, txHash)
	if !found {
		return nil, types.ErrCaseNotFound
	}
	return &types.QueryActiveTxSanctionResponse{Sanction: &sanction}, nil
}

func (k Keeper) FrozenAddress(goCtx context.Context, req *types.QueryFrozenAddressRequest) (*types.QueryFrozenAddressResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	freeze, found := k.GetFreezeRecord(ctx, req.Address)
	if !found {
		return nil, types.ErrCaseNotFound
	}
	return &types.QueryFrozenAddressResponse{Freeze: &freeze}, nil
}

func (k Keeper) ExecutionRecord(goCtx context.Context, req *types.QueryExecutionRecordRequest) (*types.QueryExecutionRecordResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	record, found := k.GetExecutionRecord(ctx, req.CaseId)
	if !found {
		return nil, types.ErrCaseNotFound
	}
	return &types.QueryExecutionRecordResponse{Record: &record}, nil
}
