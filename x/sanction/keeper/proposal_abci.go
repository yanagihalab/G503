package keeper

import (
	"context"

	abci "github.com/cometbft/cometbft/abci/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k Keeper) PrepareProposalHandler() sdk.PrepareProposalHandler {
	return func(ctx sdk.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
		kept, _ := k.FilterSanctionedTxs(ctx, req.Txs)
		return &abci.ResponsePrepareProposal{Txs: kept}, nil
	}
}

func (k Keeper) ProcessProposalHandler() sdk.ProcessProposalHandler {
	return func(ctx sdk.Context, req *abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
		if _, found := k.ContainsSanctionedTx(ctx, req.Txs); found {
			return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_REJECT}, nil
		}
		return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_ACCEPT}, nil
	}
}

func (k Keeper) CheckTxAllowed(ctx context.Context, sdkCtx sdk.Context, tx []byte) error {
	hash := TxHash(tx)
	if _, found := k.GetActiveTxSanction(sdkCtx, hash); found {
		return sdkerrorsTxSanctioned(hash)
	}
	return nil
}

func sdkerrorsTxSanctioned(hash []byte) error {
	return ErrSanctionedTx{Hash: hash}
}

type ErrSanctionedTx struct {
	Hash []byte
}

func (e ErrSanctionedTx) Error() string {
	return "transaction is actively sanctioned"
}
