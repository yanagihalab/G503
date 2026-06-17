package keeper

import (
	"context"
	"encoding/binary"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/yanagihalab/G503/x/sanction/types"
)

type Keeper struct {
	storeKey   storetypes.StoreKey
	authority  string
	chainID    string
	bankKeeper BankKeeper
}

func NewKeeper(storeKey storetypes.StoreKey, authority string, chainID string) Keeper {
	return Keeper{
		storeKey:  storeKey,
		authority: authority,
		chainID:   chainID,
	}
}

type BankKeeper interface {
	SendCoins(ctx context.Context, fromAddr sdk.AccAddress, toAddr sdk.AccAddress, amt sdk.Coins) error
	SendCoinsFromAccountToModule(ctx context.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins) error
}

func (k Keeper) WithBankKeeper(bankKeeper BankKeeper) Keeper {
	k.bankKeeper = bankKeeper
	return k
}

func (k Keeper) GetAuthority() string {
	return k.authority
}

func (k Keeper) GetChainID(ctx sdk.Context) string {
	if k.chainID != "" {
		return k.chainID
	}
	return ctx.ChainID()
}

func (k Keeper) Logger(ctx sdk.Context) log.Logger {
	return ctx.Logger().With("module", types.ModuleName)
}

func (k Keeper) Store(ctx sdk.Context) storetypes.KVStore {
	return ctx.KVStore(k.storeKey)
}

func uint64Key(value uint64) []byte {
	var bz [8]byte
	binary.BigEndian.PutUint64(bz[:], value)
	return bz[:]
}
