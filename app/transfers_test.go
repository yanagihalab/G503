package app_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"cosmossdk.io/log/v2"
	abci "github.com/cometbft/cometbft/abci/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"

	g503app "github.com/yanagihalab/G503/app"
	"github.com/yanagihalab/G503/x/sanction/keeper"
)

func TestMain(m *testing.M) {
	// Configure before any SDK address enters the process-wide Bech32 cache.
	cfg := sdk.GetConfig()
	cfg.SetBech32PrefixForAccount("g503", "g503pub")
	cfg.SetBech32PrefixForValidator("g503valoper", "g503valoperpub")
	cfg.SetBech32PrefixForConsensusNode("g503valcons", "g503valconspub")
	os.Exit(m.Run())
}

func TestOnlySuccessfulWholeTransactionsProduceOriginalReceipts(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "second-message-fails"}[fail], func(t *testing.T) {
			sdk.GetConfig().SetBech32PrefixForAccount("g503", "g503pub")
			key := &secp256k1.PrivKey{Key: bytes.Repeat([]byte{1}, 32)}
			sender, err := sdk.Bech32ifyAddressBytes("g503", key.PubKey().Address())
			require.NoError(t, err)
			recipient := sdk.AccAddress(bytes.Repeat([]byte{2}, 20)).String()
			app := g503app.New(log.NewNopLogger(), dbm.NewMemDB(), nil, false, nil)
			state, err := json.Marshal(g503app.DefaultGenesisState(app.AppCodec(), g503app.DemoGenesisAccounts{FaucetAddress: sender, DemoAddress: recipient}))
			require.NoError(t, err)
			_, err = app.InitChain(&abci.RequestInitChain{AppStateBytes: state})
			require.NoError(t, err)
			senderBytes, err := sdk.AccAddressFromBech32(sender)
			require.NoError(t, err)
			require.NotNil(t, app.AuthKeeper.GetAccount(app.NewContext(false), senderBytes), "genesis sender")
			_, err = app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: 1, Time: time.Unix(1700000000, 0)})
			require.NoError(t, err)
			require.NotNil(t, app.AuthKeeper.GetAccount(app.NewContext(false), senderBytes), "committed genesis sender")
			_, err = app.Commit()
			require.NoError(t, err)
			second := int64(7)
			if fail {
				second = 2_000_000_000
			}
			account := app.AuthKeeper.GetAccount(app.NewContext(true), senderBytes)
			require.NotNil(t, account)
			tx := signedTxBytesForAccount(t, "", account.GetAccountNumber(), &banktypes.MsgSend{FromAddress: sender, ToAddress: recipient, Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", 10))}, &banktypes.MsgSend{FromAddress: sender, ToAddress: recipient, Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", second))})
			check, err := app.CheckTx(&abci.RequestCheckTx{Tx: tx})
			require.NoError(t, err)
			require.Zero(t, check.Code, check.Log)
			require.Empty(t, app.SanctionKeeper.GetOriginalTransfers(app.NewContext(true), keeper.TxHash(tx)))
			resp, err := app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: 2, Time: time.Unix(1700000001, 0), Txs: [][]byte{tx}})
			require.NoError(t, err)
			require.Len(t, resp.TxResults, 1)
			_, err = app.Commit()
			require.NoError(t, err)
			receipts := app.SanctionKeeper.GetOriginalTransfers(app.NewContext(true), keeper.TxHash(tx))
			if fail {
				require.NotZero(t, resp.TxResults[0].Code)
				require.Empty(t, receipts, "a partially executed or failed tx must not be usable for recovery")
			} else {
				require.Zero(t, resp.TxResults[0].Code, resp.TxResults[0].Log)
				require.Len(t, receipts, 2)
				require.Equal(t, uint32(0), receipts[0].MessageIndex)
				require.Equal(t, uint32(1), receipts[1].MessageIndex)
				require.Equal(t, uint64(2), receipts[0].Height)
			}
		})
	}
}
