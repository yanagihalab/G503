package types

import (
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

var (
	amino     = codec.NewLegacyAmino()
	ModuleCdc = codec.NewProtoCodec(types.NewInterfaceRegistry())
)

func RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {
	cdc.RegisterConcrete(&MsgRegisterAgent{}, "sanction/RegisterAgent", nil)
	cdc.RegisterConcrete(&MsgSubmitRiskReport{}, "sanction/SubmitRiskReport", nil)
	cdc.RegisterConcrete(&MsgOpenSanctionCase{}, "sanction/OpenSanctionCase", nil)
	cdc.RegisterConcrete(&MsgSubmitSanctionVote{}, "sanction/SubmitSanctionVote", nil)
	cdc.RegisterConcrete(&MsgExecuteSanction{}, "sanction/ExecuteSanction", nil)
	cdc.RegisterConcrete(&MsgRevokeSanction{}, "sanction/RevokeSanction", nil)
	cdc.RegisterConcrete(&MsgUpdateParams{}, "sanction/UpdateParams", nil)
}

func RegisterInterfaces(registry types.InterfaceRegistry) {
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgRegisterAgent{},
		&MsgSubmitRiskReport{},
		&MsgOpenSanctionCase{},
		&MsgSubmitSanctionVote{},
		&MsgExecuteSanction{},
		&MsgRevokeSanction{},
		&MsgUpdateParams{},
	)
}

func init() {
	RegisterLegacyAminoCodec(amino)
	amino.Seal()
}
