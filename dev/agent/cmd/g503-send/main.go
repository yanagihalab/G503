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

	tmhash "github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	"github.com/yanagihalab/G503/app"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	"github.com/yanagihalab/G503/dev/agent/internal/rpcguard"
)

type output struct {
	Message     string                       `json:"message"`
	From        string                       `json:"from"`
	To          string                       `json:"to"`
	Amount      string                       `json:"amount"`
	Account     uint64                       `json:"account_number"`
	Sequence    uint64                       `json:"sequence"`
	TxHash      string                       `json:"tx_hash"`
	TxBytesHex  string                       `json:"tx_bytes_hex,omitempty"`
	Broadcast   *chainclient.BroadcastResult `json:"broadcast,omitempty"`
	MempoolSeen bool                         `json:"mempool_seen"`
}

func main() {
	initSDKConfig()
	var guardOptions rpcguard.Options
	guardOptions.Flags(flag.CommandLine)
	var (
		rpcURL        = flag.String("rpc-url", "http://127.0.0.1:26657", "CometBFT RPC URL")
		chainID       = flag.String("chain-id", "g503-local-1", "chain ID used for signing")
		from          = flag.String("from", "", "sender bech32 address; derived from -privkey-hex when omitted")
		to            = flag.String("to", "", "recipient bech32 address")
		amount        = flag.String("amount", "1stake", "amount to send, for example 10stake")
		privKeyHex    = flag.String("privkey-hex", "", "sender secp256k1 private key hex")
		accountNumber = flag.Uint64("account-number", 0, "sender account number")
		sequence      = flag.Uint64("sequence", 0, "sender account sequence")
		gasLimit      = flag.Uint64("gas", 200000, "gas limit")
		fee           = flag.String("fee", "0stake", "fee amount, for example 1000stake")
		checkMempool  = flag.Bool("check-mempool", true, "poll /unconfirmed_txs until the tx hash appears")
		dryRun        = flag.Bool("dry-run", false, "encode and print signed tx bytes without broadcasting")
	)
	flag.Parse()
	if *privKeyHex == "" {
		log.Fatal("-privkey-hex is required")
	}
	if *to == "" {
		log.Fatal("-to is required")
	}
	sender := *from
	if sender == "" {
		var err error
		sender, err = addressFromPrivateKeyHex(*privKeyHex)
		if err != nil {
			log.Fatal(err)
		}
	}
	coins, err := sdk.ParseCoinsNormalized(*amount)
	if err != nil {
		log.Fatal(err)
	}
	fees, err := sdk.ParseCoinsNormalized(*fee)
	if err != nil {
		log.Fatal(err)
	}
	msg := &banktypes.MsgSend{
		FromAddress: sender,
		ToAddress:   *to,
		Amount:      coins,
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
	if *dryRun {
		writeOutput(output{
			Message:     "bank.MsgSend signed tx encoded",
			From:        sender,
			To:          *to,
			Amount:      coins.String(),
			Account:     *accountNumber,
			Sequence:    *sequence,
			TxHash:      txHash,
			TxBytesHex:  hex.EncodeToString(txBytes),
			MempoolSeen: false,
		})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	guardOptions.Namespace = sender
	guard, err := rpcguard.Open(*chainID, *rpcURL, guardOptions)
	if err != nil {
		log.Fatal(err)
	}
	defer guard.Close()
	result, err := guard.Send(ctx, guardOptions.RequestID, signOpts, msg)
	if err != nil {
		log.Fatal(err)
	}
	if guardOptions.RequestID != "" {
		receipt, err := guard.Receipt(guardOptions.RequestID)
		if err != nil {
			log.Fatal(err)
		}
		*sequence = receipt.Sequence
		if receipt.AccountNumber != nil {
			*accountNumber = *receipt.AccountNumber
		}
	}
	seen := false
	if *checkMempool {
		seen, err = waitForGuardedMempool(ctx, guard.Client, result.Hash)
		if err != nil {
			log.Fatal(err)
		}
	}
	writeOutput(output{
		Message:     "bank.MsgSend broadcast_tx_sync completed",
		From:        sender,
		To:          *to,
		Amount:      coins.String(),
		Account:     *accountNumber,
		Sequence:    *sequence,
		TxHash:      result.Hash,
		Broadcast:   &result,
		MempoolSeen: seen,
	})
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

func waitForMempool(ctx context.Context, rpcURL string, hash string) (bool, error) {
	return waitForGuardedMempool(ctx, chainclient.New(rpcURL, 5*time.Second), hash)
}

func waitForGuardedMempool(ctx context.Context, client *chainclient.Client, hash string) (bool, error) {
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := client.Status(ctx); err != nil {
			return false, err
		}
		txs, err := client.PendingTransactions(ctx, 100)
		if err != nil {
			return false, err
		}
		for _, pending := range txs {
			if strings.EqualFold(hex.EncodeToString(tmhash.Sum(pending)), hash) {
				return true, nil
			}
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline.C:
			return false, nil
		case <-ticker.C:
		}
	}
}

func writeOutput(value output) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		log.Fatal(err)
	}
}
