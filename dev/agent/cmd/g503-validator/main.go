package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"cosmossdk.io/math"
	tmhash "github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/yanagihalab/G503/app"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
)

type txOutput struct {
	Message    string                       `json:"message"`
	Action     string                       `json:"action"`
	Signer     string                       `json:"signer"`
	Validator  string                       `json:"validator"`
	Amount     string                       `json:"amount,omitempty"`
	Account    uint64                       `json:"account_number"`
	Sequence   uint64                       `json:"sequence"`
	TxHash     string                       `json:"tx_hash"`
	TxBytesHex string                       `json:"tx_bytes_hex,omitempty"`
	Broadcast  *chainclient.BroadcastResult `json:"broadcast,omitempty"`
}

func main() {
	initSDKConfig()
	if len(os.Args) < 2 {
		log.Fatal("usage: g503-validator <create-validator|unbond|unjail> [flags]")
	}
	action := os.Args[1]
	fs := flag.NewFlagSet(action, flag.ExitOnError)
	rpcURL := fs.String("rpc-url", "http://127.0.0.1:26657", "CometBFT RPC URL")
	chainID := fs.String("chain-id", "g503-local-1", "chain ID used for signing")
	privKeyHex := fs.String("privkey-hex", "", "validator operator secp256k1 private key hex")
	accountNumber := fs.Uint64("account-number", 0, "signer account number")
	sequence := fs.Uint64("sequence", 0, "signer account sequence")
	gasLimit := fs.Uint64("gas", 500000, "gas limit")
	fee := fs.String("fee", "0stake", "fee amount, for example 1000stake")
	amount := fs.String("amount", "1000000stake", "bond amount")
	pubkeyFile := fs.String("pubkey-file", "", "consensus pubkey JSON from `g503d comet show-validator`")
	moniker := fs.String("moniker", "validator", "validator moniker")
	dryRun := fs.Bool("dry-run", false, "encode and print signed tx bytes without broadcasting")
	if err := fs.Parse(os.Args[2:]); err != nil {
		log.Fatal(err)
	}
	if *privKeyHex == "" {
		log.Fatal("-privkey-hex is required")
	}
	signer, err := addressFromPrivateKeyHex(*privKeyHex)
	if err != nil {
		log.Fatal(err)
	}
	valAddr := sdk.ValAddress(sdk.MustAccAddressFromBech32(signer)).String()
	msg, amountString, err := buildMsg(action, signer, valAddr, *amount, *pubkeyFile, *moniker)
	if err != nil {
		log.Fatal(err)
	}
	fees, err := sdk.ParseCoinsNormalized(*fee)
	if err != nil {
		log.Fatal(err)
	}
	signOpts := chainclient.SignOptions{
		ChainID:       *chainID,
		PrivateKeyHex: *privKeyHex,
		AccountNumber: *accountNumber,
		Sequence:      *sequence,
		GasLimit:      *gasLimit,
		FeeAmount:     fees,
	}
	txBytes, err := chainclient.EncodeSignedTx(signOpts, msg)
	if err != nil {
		log.Fatal(err)
	}
	txHash := strings.ToUpper(hex.EncodeToString(tmhash.Sum(txBytes)))
	out := txOutput{
		Message:    "validator lifecycle tx encoded",
		Action:     action,
		Signer:     signer,
		Validator:  valAddr,
		Amount:     amountString,
		Account:    *accountNumber,
		Sequence:   *sequence,
		TxHash:     txHash,
		TxBytesHex: hex.EncodeToString(txBytes),
	}
	if *dryRun {
		writeOutput(out)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := chainclient.New(*rpcURL, 10*time.Second).BroadcastTxSync(ctx, txBytes)
	if err != nil {
		log.Fatal(err)
	}
	out.Message = "validator lifecycle tx broadcast_tx_sync completed"
	out.Broadcast = &result
	writeOutput(out)
}

func buildMsg(action string, signer string, valAddr string, amount string, pubkeyFile string, moniker string) (sdk.Msg, string, error) {
	switch action {
	case "create-validator":
		if pubkeyFile == "" {
			return nil, "", fmt.Errorf("-pubkey-file is required for create-validator")
		}
		pubKey, err := readConsensusPubKey(pubkeyFile)
		if err != nil {
			return nil, "", err
		}
		coin, err := sdk.ParseCoinNormalized(amount)
		if err != nil {
			return nil, "", err
		}
		commission := stakingtypes.NewCommissionRates(
			math.LegacyMustNewDecFromStr("0.1"),
			math.LegacyMustNewDecFromStr("0.2"),
			math.LegacyMustNewDecFromStr("0.01"),
		)
		msg, err := stakingtypes.NewMsgCreateValidator(
			valAddr,
			pubKey,
			coin,
			stakingtypes.NewDescription(moniker, "", "", "", ""),
			commission,
			math.OneInt(),
		)
		return msg, coin.String(), err
	case "unbond":
		coin, err := sdk.ParseCoinNormalized(amount)
		if err != nil {
			return nil, "", err
		}
		return stakingtypes.NewMsgUndelegate(signer, valAddr, coin), coin.String(), nil
	case "unjail":
		return slashingtypes.NewMsgUnjail(valAddr), "", nil
	default:
		return nil, "", fmt.Errorf("unknown action %q", action)
	}
}

func readConsensusPubKey(path string) (cryptotypes.PubKey, error) {
	bz, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pubKey cryptotypes.PubKey
	encoding := app.MakeEncodingConfig()
	if err := encoding.Codec.UnmarshalInterfaceJSON(bz, &pubKey); err != nil {
		return nil, err
	}
	return pubKey, nil
}

func initSDKConfig() {
	cfg := sdk.GetConfig()
	cfg.SetBech32PrefixForAccount(app.AccountAddressPrefix, app.AccountAddressPrefix+"pub")
	cfg.SetBech32PrefixForValidator(app.AccountAddressPrefix+"valoper", app.AccountAddressPrefix+"valoperpub")
	cfg.SetBech32PrefixForConsensusNode(app.AccountAddressPrefix+"valcons", app.AccountAddressPrefix+"valconspub")
	cfg.Seal()
}

func addressFromPrivateKeyHex(privateKeyHex string) (string, error) {
	keyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return "", err
	}
	if len(keyBytes) != secp256k1.PrivKeySize {
		return "", fmt.Errorf("secp256k1 private key must be %d bytes", secp256k1.PrivKeySize)
	}
	priv := &secp256k1.PrivKey{Key: keyBytes}
	return sdk.AccAddress(priv.PubKey().Address()).String(), nil
}

func writeOutput(value txOutput) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		log.Fatal(err)
	}
}
