package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"cosmossdk.io/log/v2"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"

	g503app "github.com/yanagihalab/G503/app"
	"github.com/yanagihalab/G503/x/sanction/keeper"
	sanctiontypes "github.com/yanagihalab/G503/x/sanction/types"
)

func TestEncodingConfigRegistersSanctionMessages(t *testing.T) {
	encoding := g503app.MakeEncodingConfig()
	implementations := encoding.InterfaceRegistry.ListImplementations("cosmos.base.v1beta1.Msg")
	require.Contains(t, implementations, "/sanction.v1.MsgSubmitRiskReport")
	require.NoError(t, encoding.InterfaceRegistry.EnsureRegistered(&sanctiontypes.MsgSubmitRiskReport{}))
	require.NoError(t, encoding.InterfaceRegistry.EnsureRegistered(&sanctiontypes.MsgExecuteSanction{}))
}

func TestLeaseFenceIsRecheckedAfterAdmissionAndCannotBeOmitted(t *testing.T) {
	app := g503app.New(log.NewNopLogger(), dbm.NewMemDB(), nil, false, nil)
	_, err := app.InitChain(&abci.RequestInitChain{})
	require.NoError(t, err)
	ctx := app.NewContext(false).WithBlockHeight(5)
	key := &secp256k1.PrivKey{Key: bytes.Repeat([]byte{1}, 32)}
	address := sdk.AccAddress(key.PubKey().Address()).String()
	lease := &sanctiontypes.AgentLease{SignerAddress: address, HolderId: strings.Repeat("a", 64), Epoch: 1, ExpiresHeight: 10}
	app.SanctionKeeper.SetAgentLease(ctx, lease)
	encoding := g503app.MakeEncodingConfig()
	builder := encoding.TxConfig.NewTxBuilder()
	require.NoError(t, builder.SetMsgs(&banktypes.MsgSend{FromAddress: address, ToAddress: address, Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", 1))}))
	require.NoError(t, builder.SetSignatures(signing.SignatureV2{PubKey: key.PubKey(), Data: &signing.SingleSignatureData{SignMode: signing.SignMode_SIGN_MODE_DIRECT, Signature: []byte{1}}}))
	conditional := builder.GetTx()
	builder.SetMemo(keeper.LeaseMemo(lease))
	require.NoError(t, app.SanctionKeeper.CheckAgentLeaseTx(ctx, conditional))
	lease.Epoch = 2
	lease.HolderId = strings.Repeat("b", 64)
	app.SanctionKeeper.SetAgentLease(ctx, lease)
	require.Error(t, app.SanctionKeeper.CheckAgentLeaseTx(ctx, conditional), "a previously admitted old owner's tx must be fenced at delivery")
	builder.SetMemo(keeper.LeaseMemo(lease))
	require.NoError(t, app.SanctionKeeper.CheckAgentLeaseTx(ctx, builder.GetTx()))
	require.Error(t, app.SanctionKeeper.CheckAgentLeaseTx(ctx.WithBlockHeight(10), builder.GetTx()))
	builder.SetMemo("")
	require.Error(t, app.SanctionKeeper.CheckAgentLeaseTx(ctx, builder.GetTx()))
	require.NoError(t, builder.SetSignatures(signing.SignatureV2{Data: &signing.SingleSignatureData{SignMode: signing.SignMode_SIGN_MODE_DIRECT, Signature: []byte{1}}}))
	require.ErrorIs(t, app.SanctionKeeper.CheckAgentLeaseTx(ctx, builder.GetTx()), sanctiontypes.ErrLeaseFenced, "omitted public key must not bypass the required signer fence")
	builder.SetMemo(keeper.LeaseMemo(lease))
	require.NoError(t, app.SanctionKeeper.CheckAgentLeaseTx(ctx, builder.GetTx()), "standard SDK verification may load a cached public key")
	require.NoError(t, builder.SetMsgs(&sanctiontypes.MsgClaimAgentLease{SignerAddress: address}, &banktypes.MsgSend{FromAddress: address}))
	require.Error(t, app.SanctionKeeper.CheckAgentLeaseTx(ctx, builder.GetTx()), "claim exemption cannot wrap another operation")
	other := sdk.AccAddress(bytes.Repeat([]byte{2}, 20)).String()
	require.NoError(t, builder.SetMsgs(&sanctiontypes.MsgClaimAgentLease{SignerAddress: other}))
	builder.SetFeePayer(sdk.MustAccAddressFromBech32(address))
	builder.SetMemo("")
	require.ErrorIs(t, app.SanctionKeeper.CheckAgentLeaseTx(ctx, builder.GetTx()), sanctiontypes.ErrLeaseFenced, "a different leased fee payer is not exempt from fencing")
}

func TestSanctionSignedTxDecodesInCheckTx(t *testing.T) {
	app := g503app.New(log.NewNopLogger(), dbm.NewMemDB(), nil, false, nil)
	_, err := app.InitChain(&abci.RequestInitChain{})
	require.NoError(t, err)

	txHash := bytes.Repeat([]byte{0x44}, 32)
	txBytes := signedTxBytes(t, "", &sanctiontypes.MsgSubmitRiskReport{
		Submitter: "g50310xcqpzrky6eff2g52qdye53xkk9jxkvrnsxwnj",
		Report: &sanctiontypes.RiskReport{
			ReportId:       "report-checktx",
			TxHash:         txHash,
			Recipient:      "g5031zydx08dkumna6kxl0f2993zhth0zz9sdu9zrjw",
			Amount:         "10",
			Denom:          "stake",
			RiskScore:      95,
			RiskCategories: []string{"scam"},
			PolicyVersion:  "poc-v1",
		},
	}, &sanctiontypes.MsgOpenSanctionCase{
		Submitter:       "g50310xcqpzrky6eff2g52qdye53xkk9jxkvrnsxwnj",
		CaseId:          "case-checktx",
		TxHash:          txHash,
		TargetType:      sanctiontypes.SanctionTargetType_SANCTION_TARGET_TYPE_TX,
		RequestedAction: sanctiontypes.SanctionAction_SANCTION_ACTION_BLOCK_TX,
		ReportIds:       []string{"report-checktx"},
		PolicyVersion:   "poc-v1",
	}, &sanctiontypes.MsgExecuteSanction{
		Executor: "g50310xcqpzrky6eff2g52qdye53xkk9jxkvrnsxwnj",
		CaseId:   "case-checktx",
	})

	resp, err := app.CheckTx(&abci.RequestCheckTx{Tx: txBytes})
	require.NoError(t, err)
	require.NotContains(t, resp.Log, "unable to resolve type URL")
}

func signedTxBytes(t *testing.T, chainID string, msgs ...sdk.Msg) []byte {
	return signedTxBytesForAccount(t, chainID, 0, msgs...)
}

func signedTxBytesForAccount(t *testing.T, chainID string, accountNumber uint64, msgs ...sdk.Msg) []byte {
	t.Helper()

	priv := &secp256k1.PrivKey{Key: bytes.Repeat([]byte{0x01}, secp256k1.PrivKeySize)}
	encoding := g503app.MakeEncodingConfig()
	builder := encoding.TxConfig.NewTxBuilder()
	require.NoError(t, builder.SetMsgs(msgs...))
	builder.SetGasLimit(200000)
	require.NoError(t, builder.SetSignatures(signing.SignatureV2{
		PubKey: priv.PubKey(),
		Data: &signing.SingleSignatureData{
			SignMode:  signing.SignMode_SIGN_MODE_DIRECT,
			Signature: nil,
		},
		Sequence: 0,
	}))
	sig, err := clienttx.SignWithPrivKey(
		context.Background(),
		signing.SignMode_SIGN_MODE_DIRECT,
		authsigning.SignerData{
			Address:       sdk.AccAddress(priv.PubKey().Address()).String(),
			ChainID:       chainID,
			AccountNumber: accountNumber,
			Sequence:      0,
			PubKey:        priv.PubKey(),
		},
		builder,
		priv,
		encoding.TxConfig,
		0,
	)
	require.NoError(t, err)
	require.NoError(t, builder.SetSignatures(sig))
	txBytes, err := encoding.TxConfig.TxEncoder()(builder.GetTx())
	require.NoError(t, err)
	return txBytes
}

func TestSanctionAppProposalHandlersFilterActiveSanctions(t *testing.T) {
	app := g503app.New(log.NewNopLogger(), dbm.NewMemDB(), nil, false, nil)
	_, err := app.InitChain(&abci.RequestInitChain{})
	require.NoError(t, err)
	ctx := app.NewUncachedContext(false, cmtproto.Header{})

	blockedTx := []byte("blocked")
	allowedTx := []byte("allowed")
	blockedHash := keeper.TxHash(blockedTx)
	app.SanctionKeeper.SetActiveTxSanction(ctx, &sanctiontypes.ActiveSanction{
		CaseId: "case-blocked",
		TxHash: blockedHash,
		Action: sanctiontypes.SanctionAction_SANCTION_ACTION_BLOCK_TX,
	})

	prepareResp, err := app.PrepareProposal(&abci.RequestPrepareProposal{
		Height: 1,
		Txs:    [][]byte{blockedTx, allowedTx},
	})
	require.NoError(t, err)
	require.Equal(t, [][]byte{allowedTx}, prepareResp.Txs)

	processResp, err := app.ProcessProposal(&abci.RequestProcessProposal{
		Height: 1,
		Txs:    [][]byte{blockedTx},
	})
	require.NoError(t, err)
	require.Equal(t, abci.ResponseProcessProposal_REJECT, processResp.Status)
}

func TestBankMessageRejectsFrozenSenderAndRecipient(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("g503", "g503pub")
	sender := sdk.AccAddress(bytes.Repeat([]byte{0x11}, 20))
	recipient := sdk.AccAddress(bytes.Repeat([]byte{0x22}, 20))
	app := g503app.New(log.NewNopLogger(), dbm.NewMemDB(), nil, false, nil)
	genesis := g503app.DefaultGenesisState(app.AppCodec(), g503app.DemoGenesisAccounts{FaucetAddress: sender.String(), DemoAddress: recipient.String()})
	state, err := json.Marshal(genesis)
	require.NoError(t, err)
	_, err = app.InitChain(&abci.RequestInitChain{AppStateBytes: state})
	require.NoError(t, err)
	ctx := app.NewContext(false)
	msg := &banktypes.MsgSend{FromAddress: sender.String(), ToAddress: recipient.String(), Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", 10))}
	server := bankkeeper.NewMsgServerImpl(app.BankKeeper)
	_, err = server.Send(ctx, msg)
	require.NoError(t, err)
	for _, address := range []sdk.AccAddress{sender, recipient} {
		before := app.BankKeeper.GetBalance(ctx, sender, "stake")
		app.SanctionKeeper.SetFreezeRecord(ctx, &sanctiontypes.FreezeRecord{CaseId: "freeze", Address: address.String()})
		_, err = server.Send(ctx, msg)
		require.ErrorIs(t, err, sanctiontypes.ErrAddressAlreadyFrozen)
		require.Equal(t, before, app.BankKeeper.GetBalance(ctx, sender, "stake"))
		app.SanctionKeeper.DeleteFreezeRecord(ctx, address.String())
	}
	_, err = server.Send(ctx, msg)
	require.NoError(t, err)
}

func TestApprovedSettlementCanMoveFrozenFundsWithoutUnfreezing(t *testing.T) {
	for _, action := range []sanctiontypes.SanctionAction{sanctiontypes.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER, sanctiontypes.SanctionAction_SANCTION_ACTION_ESCROW_FUNDS} {
		t.Run(action.String(), func(t *testing.T) {
			sdk.GetConfig().SetBech32PrefixForAccount("g503", "g503pub")
			sender := sdk.AccAddress(bytes.Repeat([]byte{0x11}, 20))
			recipient := sdk.AccAddress(bytes.Repeat([]byte{0x22}, 20))
			app := g503app.New(log.NewNopLogger(), dbm.NewMemDB(), nil, false, nil)
			state, err := json.Marshal(g503app.DefaultGenesisState(app.AppCodec(), g503app.DemoGenesisAccounts{FaucetAddress: sender.String(), DemoAddress: recipient.String()}))
			require.NoError(t, err)
			_, err = app.InitChain(&abci.RequestInitChain{AppStateBytes: state})
			require.NoError(t, err)
			ctx := app.NewContext(false).WithBlockHeight(10).WithChainID("settlement-test")
			server := keeper.NewMsgServerImpl(app.SanctionKeeper)
			report := &sanctiontypes.RiskReport{ReportId: "settlement-report", TxHash: bytes.Repeat([]byte{0xab}, 32), Sender: sender.String(), Recipient: recipient.String(), Amount: "10", Denom: "stake", RiskScore: 95, RiskCategories: []string{"scam"}, EvidenceHash: bytes.Repeat([]byte{1}, 32), Source: "chainalysis-mock", ObservedHeight: 10, ExpiryHeight: 40}
			app.SanctionKeeper.SetOriginalTransfer(ctx, &sanctiontypes.OriginalTransfer{TxHash: report.TxHash, Sender: report.Sender, Recipient: report.Recipient, Amount: report.Amount, Denom: report.Denom, Height: 9})
			_, err = server.SubmitRiskReport(ctx, &sanctiontypes.MsgSubmitRiskReport{Submitter: sender.String(), Report: report})
			require.NoError(t, err)
			keys := make([]*secp256k1.PrivKey, 5)
			for i := range keys {
				keys[i] = secp256k1.GenPrivKey()
				agent := &sanctiontypes.AgentInfo{AgentId: fmt.Sprintf("agent-%d", i), SignerAddress: sdk.AccAddress(keys[i].PubKey().Address()).String(), PublicKey: keys[i].PubKey().Bytes(), VotingPower: 1, Active: true}
				_, err = server.RegisterAgent(ctx, &sanctiontypes.MsgRegisterAgent{Authority: app.SanctionKeeper.GetAuthority(ctx), Agent: agent})
				require.NoError(t, err)
			}
			if action == sanctiontypes.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER {
				_, err = server.OpenSanctionCase(ctx, &sanctiontypes.MsgOpenSanctionCase{Submitter: sender.String(), CaseId: "settlement", TxHash: report.TxHash, TargetType: sanctiontypes.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS, TargetAddress: recipient.String(), RequestedAction: action, ReportIds: []string{report.ReportId}})
				require.NoError(t, err)
			} else {
				// Escrow has no default policy threshold; exercise an explicitly proposed case.
				c := &sanctiontypes.SanctionCase{CaseId: "settlement", TxHash: report.TxHash, TargetAddress: recipient.String(), RequestedAction: action, ReportIds: []string{report.ReportId}, Status: sanctiontypes.CaseStatus_CASE_STATUS_PENDING, VotingDeadlineHeight: 40}
				require.NoError(t, app.SanctionKeeper.SnapshotCaseVoters(ctx, c))
				app.SanctionKeeper.SetSanctionCase(ctx, c)
			}
			for _, address := range []sdk.AccAddress{sender, recipient} {
				app.SanctionKeeper.SetFreezeRecord(ctx, &sanctiontypes.FreezeRecord{CaseId: "freeze-" + address.String(), Address: address.String()})
			}
			request := &sanctiontypes.MsgExecuteSanction{Executor: sender.String(), CaseId: "settlement"}
			_, err = server.ExecuteSanction(ctx, request)
			require.ErrorIs(t, err, sanctiontypes.ErrQuorumNotReached)

			for i, key := range keys {
				vote := &sanctiontypes.SanctionVote{CaseId: "settlement", AgentId: fmt.Sprintf("agent-%d", i), Option: sanctiontypes.VoteOption_VOTE_OPTION_APPROVE, ApprovedAction: action, SignedHeight: 10}
				vote.Signature, err = key.Sign(keeper.VoteSignBytes("settlement-test", vote))
				require.NoError(t, err)
				_, err = server.SubmitSanctionVote(ctx, &sanctiontypes.MsgSubmitSanctionVote{Submitter: sdk.AccAddress(key.PubKey().Address()).String(), Vote: vote})
				if action == sanctiontypes.SanctionAction_SANCTION_ACTION_ESCROW_FUNDS && i == 4 {
					// Threshold approval closes non-revert voting after four of five votes.
					require.ErrorIs(t, err, sanctiontypes.ErrCaseNotPending)
				} else {
					require.NoError(t, err)
				}
				if action == sanctiontypes.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER && i == 3 {
					_, err = server.ExecuteSanction(ctx, request)
					require.ErrorIs(t, err, sanctiontypes.ErrQuorumNotReached)
				}
			}
			to := sender
			if action == sanctiontypes.SanctionAction_SANCTION_ACTION_ESCROW_FUNDS {
				to = authtypes.NewModuleAddress(sanctiontypes.ModuleName)
			}
			beforeFrom := app.BankKeeper.GetBalance(ctx, recipient, "stake")
			beforeTo := app.BankKeeper.GetBalance(ctx, to, "stake")
			response, err := server.ExecuteSanction(ctx, request)
			require.NoError(t, err)
			require.NotEmpty(t, response.Record.StateChangeHash)
			require.Equal(t, beforeFrom.Sub(sdk.NewInt64Coin("stake", 10)), app.BankKeeper.GetBalance(ctx, recipient, "stake"))
			require.Equal(t, beforeTo.Add(sdk.NewInt64Coin("stake", 10)), app.BankKeeper.GetBalance(ctx, to, "stake"))
			for _, address := range []sdk.AccAddress{sender, recipient} {
				require.True(t, app.SanctionKeeper.IsAddressFrozen(ctx, address.String()))
			}
			_, err = bankkeeper.NewMsgServerImpl(app.BankKeeper).Send(ctx, &banktypes.MsgSend{FromAddress: recipient.String(), ToAddress: sender.String(), Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", 1))})
			require.ErrorIs(t, err, sanctiontypes.ErrAddressAlreadyFrozen)
			_, err = server.ExecuteSanction(ctx, request)
			require.Error(t, err)
		})
	}
}
