package app

import (
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	authcodec "github.com/cosmos/cosmos-sdk/codec/address"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/std"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	consensustypes "github.com/cosmos/cosmos-sdk/x/consensus/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/cosmos/cosmos-sdk/x/tx/signing"
	"github.com/cosmos/gogoproto/proto"

	sanctiontypes "github.com/yanagihalab/G503/x/sanction/types"
)

type EncodingConfig struct {
	InterfaceRegistry codectypes.InterfaceRegistry
	Codec             codec.Codec
	TxConfig          client.TxConfig
	LegacyAmino       *codec.LegacyAmino
}

func MakeEncodingConfig() EncodingConfig {
	legacyAmino := codec.NewLegacyAmino()
	interfaceRegistry, err := codectypes.NewInterfaceRegistryWithOptions(codectypes.InterfaceRegistryOptions{
		ProtoFiles: proto.HybridResolver,
		SigningOptions: signing.Options{
			AddressCodec:          authcodec.NewBech32Codec(AccountAddressPrefix),
			ValidatorAddressCodec: authcodec.NewBech32Codec(AccountAddressPrefix + "valoper"),
		},
	})
	if err != nil {
		panic(err)
	}

	std.RegisterLegacyAminoCodec(legacyAmino)
	std.RegisterInterfaces(interfaceRegistry)
	authtypes.RegisterLegacyAminoCodec(legacyAmino)
	authtypes.RegisterInterfaces(interfaceRegistry)
	stakingtypes.RegisterLegacyAminoCodec(legacyAmino)
	stakingtypes.RegisterInterfaces(interfaceRegistry)
	slashingtypes.RegisterLegacyAminoCodec(legacyAmino)
	slashingtypes.RegisterInterfaces(interfaceRegistry)
	banktypes.RegisterInterfaces(interfaceRegistry)
	consensustypes.RegisterInterfaces(interfaceRegistry)
	sanctiontypes.RegisterLegacyAminoCodec(legacyAmino)
	sanctiontypes.RegisterInterfaces(interfaceRegistry)

	cdc := codec.NewProtoCodec(interfaceRegistry)
	return EncodingConfig{
		InterfaceRegistry: interfaceRegistry,
		Codec:             cdc,
		TxConfig:          authtx.NewTxConfig(cdc, authtx.DefaultSignModes),
		LegacyAmino:       legacyAmino,
	}
}
