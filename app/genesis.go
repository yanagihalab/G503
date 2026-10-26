package app

import (
	"encoding/json"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	sanctiontypes "github.com/yanagihalab/G503/x/sanction/types"
)

type DemoGenesisAccounts struct {
	FaucetAddress    string
	DemoAddress      string
	ValidatorAddress string
	AuthorityAddress string
	Agent            *sanctiontypes.AgentInfo
}

func DefaultGenesisState(cdc codec.JSONCodec, accounts DemoGenesisAccounts) map[string]json.RawMessage {
	genAccounts := authtypes.GenesisAccounts{}
	for name, perms := range moduleAccountPermissions() {
		genAccounts = append(genAccounts, authtypes.NewEmptyModuleAccount(name, perms...))
	}
	addresses := demoAccountAddresses(accounts)
	for i, address := range addresses {
		if address == "" {
			continue
		}
		addr, err := sdk.AccAddressFromBech32(address)
		if err != nil {
			panic(err)
		}
		genAccounts = append(genAccounts, authtypes.NewBaseAccount(addr, nil, uint64(i), 0))
	}

	authGenesis := authtypes.NewGenesisState(authtypes.DefaultParams(), genAccounts)
	bankGenesis := banktypes.DefaultGenesisState()
	balances := []banktypes.Balance{}
	for _, address := range addresses {
		if address == "" {
			continue
		}
		balances = append(balances, banktypes.Balance{
			Address: address,
			Coins:   sdk.NewCoins(sdk.NewCoin(BondDenom, sdkmath.NewInt(1_000_000_000))),
		})
	}
	bankGenesis.Balances = balances
	bankGenesis.Supply = sdk.NewCoins(sdk.NewCoin(BondDenom, sdkmath.NewInt(int64(len(balances))*1_000_000_000)))

	stakingGenesis := stakingtypes.DefaultGenesisState()
	stakingGenesis.Params.BondDenom = BondDenom
	slashingGenesis := slashingtypes.DefaultGenesisState()

	sanctionGenesis := sanctiontypes.DefaultGenesis()
	sanctionGenesis.Authority = accounts.AuthorityAddress
	if accounts.Agent != nil {
		sanctionGenesis.Agents = []*sanctiontypes.AgentInfo{accounts.Agent}
	}

	return map[string]json.RawMessage{
		authtypes.ModuleName:     cdc.MustMarshalJSON(authGenesis),
		banktypes.ModuleName:     cdc.MustMarshalJSON(bankGenesis),
		stakingtypes.ModuleName:  cdc.MustMarshalJSON(stakingGenesis),
		slashingtypes.ModuleName: cdc.MustMarshalJSON(slashingGenesis),
		sanctiontypes.ModuleName: cdc.MustMarshalJSON(sanctionGenesis),
	}
}

func demoAccountAddresses(accounts DemoGenesisAccounts) []string {
	addresses := []string{accounts.FaucetAddress, accounts.DemoAddress, accounts.ValidatorAddress}
	if accounts.Agent != nil && accounts.Agent.SignerAddress != "" {
		addresses = append(addresses, accounts.Agent.SignerAddress)
	}
	addresses = append(addresses, accounts.AuthorityAddress)
	seen := map[string]struct{}{}
	unique := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if address == "" {
			continue
		}
		if _, found := seen[address]; found {
			continue
		}
		seen[address] = struct{}{}
		unique = append(unique, address)
	}
	return unique
}
