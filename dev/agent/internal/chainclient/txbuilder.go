package chainclient

import (
	"context"
	"fmt"

	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"

	"github.com/yanagihalab/G503/app"
	"github.com/yanagihalab/G503/dev/agent/internal/signer"
)

type SignOptions struct {
	ChainID       string
	PrivateKeyHex string
	AccountNumber uint64
	Sequence      uint64
	GasLimit      uint64
	FeeAmount     sdk.Coins
	Memo          string
}

func EncodeUnsignedTx(msgs ...sdk.Msg) ([]byte, error) {
	encoding := app.MakeEncodingConfig()
	builder := encoding.TxConfig.NewTxBuilder()
	if err := builder.SetMsgs(msgs...); err != nil {
		return nil, err
	}
	builder.SetGasLimit(200000)
	return encoding.TxConfig.TxEncoder()(builder.GetTx())
}

func EncodeSignedTx(opts SignOptions, msgs ...sdk.Msg) ([]byte, error) {
	if opts.ChainID == "" {
		return nil, fmt.Errorf("chain id is required")
	}
	priv, err := privateKeyFromHex(opts.PrivateKeyHex)
	if err != nil {
		return nil, err
	}
	encoding := app.MakeEncodingConfig()
	builder := encoding.TxConfig.NewTxBuilder()
	if err := builder.SetMsgs(msgs...); err != nil {
		return nil, err
	}
	gasLimit := opts.GasLimit
	if gasLimit == 0 {
		gasLimit = 200000
	}
	builder.SetGasLimit(gasLimit)
	builder.SetMemo(opts.Memo)
	if opts.FeeAmount != nil {
		builder.SetFeeAmount(opts.FeeAmount)
	}
	if err := builder.SetSignatures(signing.SignatureV2{
		PubKey: priv.PubKey(),
		Data: &signing.SingleSignatureData{
			SignMode:  signing.SignMode_SIGN_MODE_DIRECT,
			Signature: nil,
		},
		Sequence: opts.Sequence,
	}); err != nil {
		return nil, err
	}
	sig, err := clienttx.SignWithPrivKey(
		context.Background(),
		signing.SignMode_SIGN_MODE_DIRECT,
		authsigning.SignerData{
			Address:       sdk.AccAddress(priv.PubKey().Address()).String(),
			ChainID:       opts.ChainID,
			AccountNumber: opts.AccountNumber,
			Sequence:      opts.Sequence,
			PubKey:        priv.PubKey(),
		},
		builder,
		priv,
		encoding.TxConfig,
		opts.Sequence,
	)
	if err != nil {
		return nil, err
	}
	if err := builder.SetSignatures(sig); err != nil {
		return nil, err
	}
	return encoding.TxConfig.TxEncoder()(builder.GetTx())
}

func (c *Client) BroadcastUnsignedMsgs(ctx context.Context, msgs ...sdk.Msg) (BroadcastResult, error) {
	txBytes, err := EncodeUnsignedTx(msgs...)
	if err != nil {
		return BroadcastResult{}, err
	}
	return c.BroadcastTxSync(ctx, txBytes)
}

func (c *Client) BroadcastSignedMsgs(ctx context.Context, opts SignOptions, msgs ...sdk.Msg) (BroadcastResult, error) {
	txBytes, err := EncodeSignedTx(opts, msgs...)
	if err != nil {
		return BroadcastResult{}, err
	}
	return c.BroadcastTxSync(ctx, txBytes)
}

func privateKeyFromHex(privateKeyHex string) (*secp256k1.PrivKey, error) {
	return signer.ParsePrivateKey(privateKeyHex)
}
