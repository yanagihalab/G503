package keeper

import (
	"bytes"
	"encoding/binary"

	"cosmossdk.io/errors"
	"github.com/cosmos/cosmos-sdk/store/v2/prefix"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	"github.com/yanagihalab/G503/x/sanction/types"
)

func transferKey(p []byte, hash []byte, index uint32, denom string) []byte {
	key := append(append([]byte{}, p...), hash...)
	key = binary.BigEndian.AppendUint32(key, index)
	return append(key, []byte(denom)...)
}

// The app post-handler invokes this in the same cache as a successful whole tx.
// Failed transactions, CheckTx and simulation must never create receipts.
func (k Keeper) RecordOriginalTransfers(ctx sdk.Context, tx sdk.Tx, simulate, success bool) (sdk.Context, error) {
	if simulate || !success || ctx.ExecMode() != sdk.ExecModeFinalize {
		return ctx, nil
	}
	hash := TxHash(ctx.TxBytes())
	for index, msg := range tx.GetMsgs() {
		send, ok := msg.(*banktypes.MsgSend)
		if !ok {
			continue
		}
		for _, coin := range send.Amount {
			k.SetOriginalTransfer(ctx, &types.OriginalTransfer{TxHash: hash, MessageIndex: uint32(index), Sender: send.FromAddress, Recipient: send.ToAddress, Denom: coin.Denom, Amount: coin.Amount.String(), Height: uint64(ctx.BlockHeight())})
		}
	}
	return ctx, nil
}

func (k Keeper) SetOriginalTransfer(ctx sdk.Context, transfer *types.OriginalTransfer) {
	k.Store(ctx).Set(transferKey(types.OriginalTransferKeyPrefix, transfer.TxHash, transfer.MessageIndex, transfer.Denom), types.ModuleCdc.MustMarshal(transfer))
}

func (k Keeper) GetOriginalTransfer(ctx sdk.Context, hash []byte, index uint32, denom string) (*types.OriginalTransfer, bool) {
	bz := k.Store(ctx).Get(transferKey(types.OriginalTransferKeyPrefix, hash, index, denom))
	var transfer = &types.OriginalTransfer{}
	if bz == nil {
		return transfer, false
	}
	types.ModuleCdc.MustUnmarshal(bz, transfer)
	return transfer, true
}

func (k Keeper) validateOriginal(ctx sdk.Context, report *types.RiskReport) (*types.OriginalTransfer, error) {
	t, found := k.GetOriginalTransfer(ctx, report.TxHash, report.MessageIndex, report.Denom)
	// A post-handler receipt at the current height has not been committed yet.
	if !found || t.Height >= uint64(ctx.BlockHeight()) || t.Sender != report.Sender || t.Recipient != report.Recipient || t.Amount != report.Amount || t.Denom != report.Denom || !bytes.Equal(t.TxHash, report.TxHash) {
		return &types.OriginalTransfer{}, types.ErrOriginalTransfer
	}
	return t, nil
}

func (k Keeper) SetSettlement(ctx sdk.Context, record *types.SettlementRecord) {
	t := record.Transfer
	k.Store(ctx).Set(transferKey(types.SettlementKeyPrefix, t.TxHash, t.MessageIndex, t.Denom), types.ModuleCdc.MustMarshal(record))
}

func (k Keeper) GetSettlement(ctx sdk.Context, hash []byte, index uint32, denom string) (*types.SettlementRecord, bool) {
	bz := k.Store(ctx).Get(transferKey(types.SettlementKeyPrefix, hash, index, denom))
	var record = &types.SettlementRecord{}
	if bz == nil {
		return record, false
	}
	types.ModuleCdc.MustUnmarshal(bz, record)
	return record, true
}

func (k Keeper) unsettledOriginal(ctx sdk.Context, report *types.RiskReport) (*types.OriginalTransfer, error) {
	t, err := k.validateOriginal(ctx, report)
	if err != nil {
		return t, err
	}
	if record, found := k.GetSettlement(ctx, t.TxHash, t.MessageIndex, t.Denom); found {
		return t, errors.Wrapf(types.ErrAlreadySettled, "case=%s", record.CaseId)
	}
	return t, nil
}

func (k Keeper) GetOriginalTransfers(ctx sdk.Context, hash []byte) []*types.OriginalTransfer {
	store := prefix.NewStore(k.Store(ctx), append(append([]byte{}, types.OriginalTransferKeyPrefix...), hash...))
	it := storetypes.KVStorePrefixIterator(store, nil)
	defer it.Close()
	out := []*types.OriginalTransfer{}
	for ; it.Valid(); it.Next() {
		var t = &types.OriginalTransfer{}
		types.ModuleCdc.MustUnmarshal(it.Value(), t)
		out = append(out, t)
	}
	return out
}

func (k Keeper) GetSettlements(ctx sdk.Context, hash []byte) []*types.SettlementRecord {
	store := prefix.NewStore(k.Store(ctx), append(append([]byte{}, types.SettlementKeyPrefix...), hash...))
	it := storetypes.KVStorePrefixIterator(store, nil)
	defer it.Close()
	out := []*types.SettlementRecord{}
	for ; it.Valid(); it.Next() {
		var r = &types.SettlementRecord{}
		types.ModuleCdc.MustUnmarshal(it.Value(), r)
		out = append(out, r)
	}
	return out
}
