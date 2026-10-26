package types

import (
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	gogoproto "github.com/cosmos/gogoproto/proto"
)

var (
	amino     = codec.NewLegacyAmino()
	ModuleCdc = codec.NewProtoCodec(types.NewInterfaceRegistry())
)

func RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {
	cdc.RegisterConcrete(&MsgClaimAgentLease{}, "sanction/ClaimAgentLease", nil)
	cdc.RegisterConcrete(&MsgRegisterAgent{}, "sanction/RegisterAgent", nil)
	cdc.RegisterConcrete(&MsgSubmitRiskReport{}, "sanction/SubmitRiskReport", nil)
	cdc.RegisterConcrete(&MsgOpenSanctionCase{}, "sanction/OpenSanctionCase", nil)
	cdc.RegisterConcrete(&MsgSubmitSanctionVote{}, "sanction/SubmitSanctionVote", nil)
	cdc.RegisterConcrete(&MsgExecuteSanction{}, "sanction/ExecuteSanction", nil)
	cdc.RegisterConcrete(&MsgRevokeSanction{}, "sanction/RevokeSanction", nil)
	cdc.RegisterConcrete(&MsgReviewSanction{}, "sanction/ReviewSanction", nil)
	cdc.RegisterConcrete(&MsgUpdateParams{}, "sanction/UpdateParams", nil)
}

func RegisterInterfaces(registry types.InterfaceRegistry) {
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgClaimAgentLease{},
		&MsgRegisterAgent{},
		&MsgSubmitRiskReport{},
		&MsgOpenSanctionCase{},
		&MsgSubmitSanctionVote{},
		&MsgExecuteSanction{},
		&MsgRevokeSanction{},
		&MsgReviewSanction{},
		&MsgUpdateParams{},
	)
}

func init() {
	RegisterLegacyAminoCodec(amino)
	registerGogoProtoTypes()
	amino.Seal()
}

func registerGogoProtoTypes() {
	gogoproto.RegisterType((*AgentLease)(nil), "sanction.v1.AgentLease")
	gogoproto.RegisterType((*MsgClaimAgentLease)(nil), "sanction.v1.MsgClaimAgentLease")
	gogoproto.RegisterType((*MsgClaimAgentLeaseResponse)(nil), "sanction.v1.MsgClaimAgentLeaseResponse")
	gogoproto.RegisterType((*QueryAgentLeaseRequest)(nil), "sanction.v1.QueryAgentLeaseRequest")
	gogoproto.RegisterType((*QueryAgentLeaseResponse)(nil), "sanction.v1.QueryAgentLeaseResponse")
	gogoproto.RegisterType((*OriginalTransfer)(nil), "sanction.v1.OriginalTransfer")
	gogoproto.RegisterType((*SettlementRecord)(nil), "sanction.v1.SettlementRecord")
	gogoproto.RegisterType((*ReviewRecord)(nil), "sanction.v1.ReviewRecord")
	gogoproto.RegisterType((*MsgReviewSanction)(nil), "sanction.v1.MsgReviewSanction")
	gogoproto.RegisterType((*MsgReviewSanctionResponse)(nil), "sanction.v1.MsgReviewSanctionResponse")
	gogoproto.RegisterType((*QueryAuthorityRequest)(nil), "sanction.v1.QueryAuthorityRequest")
	gogoproto.RegisterType((*QueryAuthorityResponse)(nil), "sanction.v1.QueryAuthorityResponse")
	gogoproto.RegisterType((*QueryOriginalTransfersRequest)(nil), "sanction.v1.QueryOriginalTransfersRequest")
	gogoproto.RegisterType((*QueryOriginalTransfersResponse)(nil), "sanction.v1.QueryOriginalTransfersResponse")
	gogoproto.RegisterType((*QuerySettlementsRequest)(nil), "sanction.v1.QuerySettlementsRequest")
	gogoproto.RegisterType((*QuerySettlementsResponse)(nil), "sanction.v1.QuerySettlementsResponse")
	gogoproto.RegisterType((*QueryReviewRequest)(nil), "sanction.v1.QueryReviewRequest")
	gogoproto.RegisterType((*QueryReviewResponse)(nil), "sanction.v1.QueryReviewResponse")
	gogoproto.RegisterType((*Params)(nil), "sanction.v1.Params")
	gogoproto.RegisterType((*GenesisState)(nil), "sanction.v1.GenesisState")
	gogoproto.RegisterType((*AgentInfo)(nil), "sanction.v1.AgentInfo")
	gogoproto.RegisterType((*RiskReport)(nil), "sanction.v1.RiskReport")
	gogoproto.RegisterType((*SanctionCase)(nil), "sanction.v1.SanctionCase")
	gogoproto.RegisterType((*SanctionVote)(nil), "sanction.v1.SanctionVote")
	gogoproto.RegisterType((*ActiveSanction)(nil), "sanction.v1.ActiveSanction")
	gogoproto.RegisterType((*FreezeRecord)(nil), "sanction.v1.FreezeRecord")
	gogoproto.RegisterType((*ExecutionRecord)(nil), "sanction.v1.ExecutionRecord")

	gogoproto.RegisterType((*MsgRegisterAgent)(nil), "sanction.v1.MsgRegisterAgent")
	gogoproto.RegisterType((*MsgRegisterAgentResponse)(nil), "sanction.v1.MsgRegisterAgentResponse")
	gogoproto.RegisterType((*MsgSubmitRiskReport)(nil), "sanction.v1.MsgSubmitRiskReport")
	gogoproto.RegisterType((*MsgSubmitRiskReportResponse)(nil), "sanction.v1.MsgSubmitRiskReportResponse")
	gogoproto.RegisterType((*MsgOpenSanctionCase)(nil), "sanction.v1.MsgOpenSanctionCase")
	gogoproto.RegisterType((*MsgOpenSanctionCaseResponse)(nil), "sanction.v1.MsgOpenSanctionCaseResponse")
	gogoproto.RegisterType((*MsgSubmitSanctionVote)(nil), "sanction.v1.MsgSubmitSanctionVote")
	gogoproto.RegisterType((*MsgSubmitSanctionVoteResponse)(nil), "sanction.v1.MsgSubmitSanctionVoteResponse")
	gogoproto.RegisterType((*MsgExecuteSanction)(nil), "sanction.v1.MsgExecuteSanction")
	gogoproto.RegisterType((*MsgExecuteSanctionResponse)(nil), "sanction.v1.MsgExecuteSanctionResponse")
	gogoproto.RegisterType((*MsgRevokeSanction)(nil), "sanction.v1.MsgRevokeSanction")
	gogoproto.RegisterType((*MsgRevokeSanctionResponse)(nil), "sanction.v1.MsgRevokeSanctionResponse")
	gogoproto.RegisterType((*MsgUpdateParams)(nil), "sanction.v1.MsgUpdateParams")
	gogoproto.RegisterType((*MsgUpdateParamsResponse)(nil), "sanction.v1.MsgUpdateParamsResponse")

	gogoproto.RegisterType((*QueryParamsRequest)(nil), "sanction.v1.QueryParamsRequest")
	gogoproto.RegisterType((*QueryParamsResponse)(nil), "sanction.v1.QueryParamsResponse")
	gogoproto.RegisterType((*QueryAgentRequest)(nil), "sanction.v1.QueryAgentRequest")
	gogoproto.RegisterType((*QueryAgentResponse)(nil), "sanction.v1.QueryAgentResponse")
	gogoproto.RegisterType((*QueryAgentsRequest)(nil), "sanction.v1.QueryAgentsRequest")
	gogoproto.RegisterType((*QueryAgentsResponse)(nil), "sanction.v1.QueryAgentsResponse")
	gogoproto.RegisterType((*QueryRiskReportRequest)(nil), "sanction.v1.QueryRiskReportRequest")
	gogoproto.RegisterType((*QueryRiskReportResponse)(nil), "sanction.v1.QueryRiskReportResponse")
	gogoproto.RegisterType((*QueryRiskReportsByTxRequest)(nil), "sanction.v1.QueryRiskReportsByTxRequest")
	gogoproto.RegisterType((*QueryRiskReportsByTxResponse)(nil), "sanction.v1.QueryRiskReportsByTxResponse")
	gogoproto.RegisterType((*QuerySanctionCaseRequest)(nil), "sanction.v1.QuerySanctionCaseRequest")
	gogoproto.RegisterType((*QuerySanctionCaseResponse)(nil), "sanction.v1.QuerySanctionCaseResponse")
	gogoproto.RegisterType((*QuerySanctionCasesByTxRequest)(nil), "sanction.v1.QuerySanctionCasesByTxRequest")
	gogoproto.RegisterType((*QuerySanctionCasesByTxResponse)(nil), "sanction.v1.QuerySanctionCasesByTxResponse")
	gogoproto.RegisterType((*QuerySanctionVotesRequest)(nil), "sanction.v1.QuerySanctionVotesRequest")
	gogoproto.RegisterType((*QuerySanctionVotesResponse)(nil), "sanction.v1.QuerySanctionVotesResponse")
	gogoproto.RegisterType((*QueryActiveTxSanctionRequest)(nil), "sanction.v1.QueryActiveTxSanctionRequest")
	gogoproto.RegisterType((*QueryActiveTxSanctionResponse)(nil), "sanction.v1.QueryActiveTxSanctionResponse")
	gogoproto.RegisterType((*QueryFrozenAddressRequest)(nil), "sanction.v1.QueryFrozenAddressRequest")
	gogoproto.RegisterType((*QueryFrozenAddressResponse)(nil), "sanction.v1.QueryFrozenAddressResponse")
	gogoproto.RegisterType((*QueryExecutionRecordRequest)(nil), "sanction.v1.QueryExecutionRecordRequest")
	gogoproto.RegisterType((*QueryExecutionRecordResponse)(nil), "sanction.v1.QueryExecutionRecordResponse")
}
