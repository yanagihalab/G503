package keeper

import (
	tmhash "github.com/cometbft/cometbft/crypto/tmhash"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

type BlockedTx struct {
	Hash []byte
	Tx   []byte
}

func TxHash(tx []byte) []byte {
	return tmhash.Sum(tx)
}

func (k Keeper) FilterSanctionedTxs(ctx sdk.Context, txs [][]byte) ([][]byte, []BlockedTx) {
	kept := make([][]byte, 0, len(txs))
	blocked := []BlockedTx{}
	for _, tx := range txs {
		hash := TxHash(tx)
		if _, found := k.GetActiveTxSanction(ctx, hash); found {
			blocked = append(blocked, BlockedTx{
				Hash: hash,
				Tx:   append([]byte(nil), tx...),
			})
			continue
		}
		kept = append(kept, tx)
	}
	return kept, blocked
}

func (k Keeper) ContainsSanctionedTx(ctx sdk.Context, txs [][]byte) (BlockedTx, bool) {
	for _, tx := range txs {
		hash := TxHash(tx)
		if _, found := k.GetActiveTxSanction(ctx, hash); found {
			return BlockedTx{
				Hash: hash,
				Tx:   append([]byte(nil), tx...),
			}, true
		}
	}
	return BlockedTx{}, false
}
