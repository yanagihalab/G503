package watcher

import (
	"testing"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"

	g503app "github.com/yanagihalab/G503/app"
)

func TestDecodeTransferFromBankSend(t *testing.T) {
	encoding := g503app.MakeEncodingConfig()
	builder := encoding.TxConfig.NewTxBuilder()
	err := builder.SetMsgs(&banktypes.MsgSend{
		FromAddress: "g5031qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqnrql8a",
		ToAddress:   "g5031p8s4h6dcded4dlaa7z97x0d4gcy0e2u0n3z7k5",
		Amount:      sdk.NewCoins(sdk.NewCoin("stake", sdkmath.NewInt(42))),
	})
	require.NoError(t, err)
	txBytes, err := encoding.TxConfig.TxEncoder()(builder.GetTx())
	require.NoError(t, err)

	transfer, found, err := DecodeTransfer(PendingTx{Bytes: txBytes, Hash: "abc"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "abc", transfer.TxHash)
	require.Equal(t, "g5031qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqnrql8a", transfer.Sender)
	require.Equal(t, "g5031p8s4h6dcded4dlaa7z97x0d4gcy0e2u0n3z7k5", transfer.Recipient)
	require.Equal(t, "42", transfer.Amount)
	require.Equal(t, "stake", transfer.Denom)
}

func TestDecodeTransfersIncludesEveryMessageAndDenom(t *testing.T) {
	encoding := g503app.MakeEncodingConfig()
	builder := encoding.TxConfig.NewTxBuilder()
	require.NoError(t, builder.SetMsgs(
		&banktypes.MsgSend{FromAddress: "sender", ToAddress: "safe", Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", 1), sdk.NewInt64Coin("token", 2))},
		&banktypes.MsgSend{FromAddress: "sender", ToAddress: "risky", Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", 3))},
	))
	encoded, err := encoding.TxConfig.TxEncoder()(builder.GetTx())
	require.NoError(t, err)
	transfers, err := DecodeTransfers(PendingTx{Bytes: encoded, Hash: "tx"})
	require.NoError(t, err)
	require.Len(t, transfers, 3)
	require.Equal(t, "token", transfers[1].Denom)
	require.Equal(t, "risky", transfers[2].Recipient)
	require.Equal(t, uint32(0), transfers[0].MessageIndex)
	require.Equal(t, uint32(0), transfers[1].MessageIndex)
	require.Equal(t, uint32(1), transfers[2].MessageIndex)
}
