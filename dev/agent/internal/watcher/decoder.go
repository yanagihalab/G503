package watcher

import (
	"fmt"

	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	"github.com/yanagihalab/G503/app"
)

func DecodeTransfer(tx PendingTx) (DecodedTransfer, bool, error) {
	transfers, err := DecodeTransfers(tx)
	if err != nil || len(transfers) == 0 {
		return DecodedTransfer{}, false, err
	}
	return transfers[0], true, nil
}

func DecodeTransfers(tx PendingTx) ([]DecodedTransfer, error) {
	encoding := app.MakeEncodingConfig()
	decoded, err := encoding.TxConfig.TxDecoder()(tx.Bytes)
	if err != nil {
		return nil, err
	}
	var transfers []DecodedTransfer
	for index, msg := range decoded.GetMsgs() {
		send, ok := msg.(*banktypes.MsgSend)
		if !ok {
			continue
		}
		for _, coin := range send.Amount {
			transfers = append(transfers, DecodedTransfer{MessageIndex: uint32(index), TxHash: tx.Hash, Sender: send.FromAddress, Recipient: send.ToAddress, Amount: coin.Amount.String(), Denom: coin.Denom})
		}
	}
	return transfers, nil
}

func MustDecodeTransfer(tx PendingTx) (DecodedTransfer, error) {
	transfer, found, err := DecodeTransfer(tx)
	if err != nil {
		return DecodedTransfer{}, err
	}
	if !found {
		return DecodedTransfer{}, fmt.Errorf("no bank MsgSend found in pending tx %s", tx.Hash)
	}
	return transfer, nil
}
