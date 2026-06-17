package keeper_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	sdkerrors "cosmossdk.io/errors"
	storetypes "cosmossdk.io/store/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdktestutil "github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/yanagihalab/G503/x/sanction/keeper"
	"github.com/yanagihalab/G503/x/sanction/types"
)

const authority = "sanction-authority"

type fixture struct {
	ctx       sdk.Context
	keeper    keeper.Keeper
	msgServer types.MsgServer
}

func newFixture(t *testing.T) fixture {
	t.Helper()

	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := sdktestutil.DefaultContextWithKeys(
		map[string]*storetypes.KVStoreKey{types.StoreKey: storeKey},
		map[string]*storetypes.TransientStoreKey{},
		map[string]*storetypes.MemoryStoreKey{},
	).WithBlockHeader(cmtproto.Header{
		Height: 10,
		Time:   time.Unix(1_700_000_000, 0),
	})

	k := keeper.NewKeeper(storeKey, authority, "sanction-test-1")
	k.SetParams(ctx, types.DefaultParams())

	return fixture{
		ctx:       ctx,
		keeper:    k,
		msgServer: keeper.NewMsgServerImpl(k),
	}
}

func TestApproveBlockTxCaseCreatesActiveSanction(t *testing.T) {
	f := newFixture(t)
	txHash := bytes.Repeat([]byte{0xab}, 32)
	agent1 := registerAgent(t, f, "agent-1")
	agent2 := registerAgent(t, f, "agent-2")
	_ = registerAgent(t, f, "agent-3")

	submitter := sdk.AccAddress(bytes.Repeat([]byte{0x09}, 20)).String()
	_, err := f.msgServer.SubmitRiskReport(sdk.WrapSDKContext(f.ctx), &types.MsgSubmitRiskReport{
		Submitter: submitter,
		Report: &types.RiskReport{
			ReportId:       "report-1",
			TxHash:         txHash,
			Sender:         sdk.AccAddress(bytes.Repeat([]byte{0x01}, 20)).String(),
			Recipient:      sdk.AccAddress(bytes.Repeat([]byte{0x02}, 20)).String(),
			Denom:          "utoken",
			Amount:         "100",
			Source:         "chainalysis-mock",
			RiskScore:      82,
			RiskCategories: []string{"scam"},
			ObservedHeight: 10,
			ExpiryHeight:   30,
			PolicyVersion:  "poc-v1",
			EvidenceHash:   []byte("evidence"),
			Submitter:      submitter,
		},
	})
	if err != nil {
		t.Fatalf("submit risk report: %v", err)
	}

	_, err = f.msgServer.OpenSanctionCase(sdk.WrapSDKContext(f.ctx), &types.MsgOpenSanctionCase{
		Submitter:       submitter,
		CaseId:          "case-1",
		TxHash:          txHash,
		TargetType:      types.SanctionTargetType_SANCTION_TARGET_TYPE_TX,
		RequestedAction: types.SanctionAction_SANCTION_ACTION_BLOCK_TX,
		ReportIds:       []string{"report-1"},
		PolicyVersion:   "poc-v1",
	})
	if err != nil {
		t.Fatalf("open sanction case: %v", err)
	}

	vote(t, f, agent1, "case-1")
	resp := vote(t, f, agent2, "case-1")
	if !resp.Approved {
		t.Fatalf("expected second vote to approve case")
	}

	sanction, found := f.keeper.GetActiveTxSanction(f.ctx, txHash)
	if !found {
		t.Fatalf("active sanction not stored")
	}
	if sanction.CaseId != "case-1" {
		t.Fatalf("unexpected case id: %s", sanction.CaseId)
	}
}

func TestOpenCaseRejectsPolicyMismatch(t *testing.T) {
	f := newFixture(t)
	submitter := sdk.AccAddress(bytes.Repeat([]byte{0x09}, 20)).String()
	txHash := bytes.Repeat([]byte{0xcd}, 32)

	_, err := f.msgServer.SubmitRiskReport(sdk.WrapSDKContext(f.ctx), &types.MsgSubmitRiskReport{
		Submitter: submitter,
		Report: &types.RiskReport{
			ReportId:       "report-low",
			TxHash:         txHash,
			Recipient:      sdk.AccAddress(bytes.Repeat([]byte{0x02}, 20)).String(),
			Source:         "chainalysis-mock",
			RiskScore:      20,
			RiskCategories: []string{"scam"},
			ExpiryHeight:   30,
			EvidenceHash:   []byte("evidence"),
			Submitter:      submitter,
		},
	})
	if err != nil {
		t.Fatalf("submit risk report: %v", err)
	}

	_, err = f.msgServer.OpenSanctionCase(sdk.WrapSDKContext(f.ctx), &types.MsgOpenSanctionCase{
		Submitter:       submitter,
		CaseId:          "case-low",
		TxHash:          txHash,
		TargetType:      types.SanctionTargetType_SANCTION_TARGET_TYPE_TX,
		RequestedAction: types.SanctionAction_SANCTION_ACTION_BLOCK_TX,
		ReportIds:       []string{"report-low"},
		PolicyVersion:   "poc-v1",
	})
	if !sdkerrors.IsOf(err, types.ErrActionNotAllowed) {
		t.Fatalf("expected action not allowed, got %v", err)
	}
}

func TestSignedRiskReportVerification(t *testing.T) {
	f := newFixture(t)
	agent := registerAgent(t, f, "agent-signer")
	txHash := bytes.Repeat([]byte{0xfa}, 32)
	report := types.RiskReport{
		ReportId:       "signed-report",
		TxHash:         txHash,
		Recipient:      sdk.AccAddress(bytes.Repeat([]byte{0x02}, 20)).String(),
		Source:         "chainalysis-mock",
		RiskScore:      86,
		RiskCategories: []string{"scam"},
		ExpiryHeight:   30,
		EvidenceHash:   []byte("evidence"),
		Submitter:      agent.info.SignerAddress,
	}
	sig, err := agent.priv.Sign(keeper.RiskReportSignBytes("sanction-test-1", report))
	if err != nil {
		t.Fatalf("sign risk report: %v", err)
	}
	report.SubmitterSignature = sig

	_, err = f.msgServer.SubmitRiskReport(sdk.WrapSDKContext(f.ctx), &types.MsgSubmitRiskReport{
		Submitter: agent.info.SignerAddress,
		Report:    &report,
	})
	if err != nil {
		t.Fatalf("submit signed report: %v", err)
	}

	report.ReportId = "bad-signed-report"
	report.SubmitterSignature = []byte("bad")
	_, err = f.msgServer.SubmitRiskReport(sdk.WrapSDKContext(f.ctx), &types.MsgSubmitRiskReport{
		Submitter: agent.info.SignerAddress,
		Report:    &report,
	})
	if !sdkerrors.IsOf(err, types.ErrInvalidSignature) {
		t.Fatalf("expected invalid signature, got %v", err)
	}
}

func TestProposalFilteringDropsActiveSanctionedTx(t *testing.T) {
	f := newFixture(t)
	blockedTx := []byte("blocked tx bytes")
	allowedTx := []byte("allowed tx bytes")
	hash := keeper.TxHash(blockedTx)
	f.keeper.SetActiveTxSanction(f.ctx, types.ActiveSanction{
		CaseId: "case-blocked",
		TxHash: hash,
		Action: types.SanctionAction_SANCTION_ACTION_BLOCK_TX,
	})

	kept, blocked := f.keeper.FilterSanctionedTxs(f.ctx, [][]byte{allowedTx, blockedTx})
	if len(kept) != 1 || !bytes.Equal(kept[0], allowedTx) {
		t.Fatalf("unexpected kept txs: %q", kept)
	}
	if len(blocked) != 1 || !bytes.Equal(blocked[0].Hash, hash) {
		t.Fatalf("unexpected blocked txs: %+v", blocked)
	}

	found, ok := f.keeper.ContainsSanctionedTx(f.ctx, [][]byte{allowedTx, blockedTx})
	if !ok || !bytes.Equal(found.Hash, hash) {
		t.Fatalf("expected sanctioned tx in proposal")
	}
}

func TestFrozenAddressSendGuard(t *testing.T) {
	f := newFixture(t)
	from := sdk.AccAddress(bytes.Repeat([]byte{0x01}, 20))
	to := sdk.AccAddress(bytes.Repeat([]byte{0x02}, 20))
	if err := f.keeper.SendCoinsAllowed(f.ctx, from, to); err != nil {
		t.Fatalf("unexpected guard error: %v", err)
	}
	f.keeper.SetFreezeRecord(f.ctx, types.FreezeRecord{
		CaseId:  "freeze-case",
		Address: to.String(),
	})
	if err := f.keeper.SendCoinsAllowed(f.ctx, from, to); !sdkerrors.IsOf(err, types.ErrAddressAlreadyFrozen) {
		t.Fatalf("expected frozen address error, got %v", err)
	}
}

func TestExecuteEscrowAndRevertUseBankKeeper(t *testing.T) {
	f := newFixture(t)
	bank := &mockBankKeeper{}
	f.keeper = f.keeper.WithBankKeeper(bank)
	f.msgServer = keeper.NewMsgServerImpl(f.keeper)
	sender := sdk.AccAddress(bytes.Repeat([]byte{0x01}, 20)).String()
	recipient := sdk.AccAddress(bytes.Repeat([]byte{0x02}, 20)).String()
	executor := sdk.AccAddress(bytes.Repeat([]byte{0x03}, 20)).String()

	f.keeper.SetRiskReport(f.ctx, types.RiskReport{
		ReportId:  "report-escrow",
		TxHash:    bytes.Repeat([]byte{0xee}, 32),
		Sender:    sender,
		Recipient: recipient,
		Amount:    "7",
		Denom:     "utoken",
		Source:    "chainalysis-mock",
	})
	f.keeper.SetSanctionCase(f.ctx, types.SanctionCase{
		CaseId:          "case-escrow",
		TxHash:          bytes.Repeat([]byte{0xee}, 32),
		RequestedAction: types.SanctionAction_SANCTION_ACTION_ESCROW_FUNDS,
		Status:          types.CaseStatus_CASE_STATUS_APPROVED,
		ReportIds:       []string{"report-escrow"},
		TargetAddress:   recipient,
	})
	resp, err := f.msgServer.ExecuteSanction(sdk.WrapSDKContext(f.ctx), &types.MsgExecuteSanction{
		Executor: executor,
		CaseId:   "case-escrow",
	})
	if err != nil {
		t.Fatalf("execute escrow: %v", err)
	}
	if resp.Record.ResultCode != "ok" || bank.accountToModuleCalls != 1 {
		t.Fatalf("escrow was not executed: record=%+v bank=%+v", resp.Record, bank)
	}

	f.keeper.SetRiskReport(f.ctx, types.RiskReport{
		ReportId:  "report-revert",
		TxHash:    bytes.Repeat([]byte{0xdd}, 32),
		Sender:    sender,
		Recipient: recipient,
		Amount:    "5",
		Denom:     "utoken",
		Source:    "chainalysis-mock",
	})
	f.keeper.SetSanctionCase(f.ctx, types.SanctionCase{
		CaseId:          "case-revert",
		TxHash:          bytes.Repeat([]byte{0xdd}, 32),
		RequestedAction: types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER,
		Status:          types.CaseStatus_CASE_STATUS_APPROVED,
		ReportIds:       []string{"report-revert"},
		TargetAddress:   recipient,
	})
	params := f.keeper.GetParams(f.ctx)
	params.UnanimousRequiredForRevert = false
	f.keeper.SetParams(f.ctx, params)

	resp, err = f.msgServer.ExecuteSanction(sdk.WrapSDKContext(f.ctx), &types.MsgExecuteSanction{
		Executor: executor,
		CaseId:   "case-revert",
	})
	if err != nil {
		t.Fatalf("execute revert: %v", err)
	}
	if resp.Record.ResultCode != "ok" || bank.sendCoinsCalls != 1 {
		t.Fatalf("revert was not executed: record=%+v bank=%+v", resp.Record, bank)
	}
}

type testAgent struct {
	info types.AgentInfo
	priv *secp256k1.PrivKey
}

type mockBankKeeper struct {
	sendCoinsCalls       int
	accountToModuleCalls int
}

func (m *mockBankKeeper) SendCoins(context.Context, sdk.AccAddress, sdk.AccAddress, sdk.Coins) error {
	m.sendCoinsCalls++
	return nil
}

func (m *mockBankKeeper) SendCoinsFromAccountToModule(context.Context, sdk.AccAddress, string, sdk.Coins) error {
	m.accountToModuleCalls++
	return nil
}

func registerAgent(t *testing.T, f fixture, agentID string) testAgent {
	t.Helper()

	priv := secp256k1.GenPrivKey()
	signer := sdk.AccAddress(priv.PubKey().Address()).String()
	agent := testAgent{
		priv: priv,
		info: types.AgentInfo{
			AgentId:       agentID,
			SignerAddress: signer,
			PublicKey:     priv.PubKey().Bytes(),
			VotingPower:   1,
			Active:        true,
		},
	}
	_, err := f.msgServer.RegisterAgent(sdk.WrapSDKContext(f.ctx), &types.MsgRegisterAgent{
		Authority: authority,
		Agent:     &agent.info,
	})
	if err != nil {
		t.Fatalf("register agent %s: %v", agentID, err)
	}
	return agent
}

func vote(t *testing.T, f fixture, agent testAgent, caseID string) *types.MsgSubmitSanctionVoteResponse {
	t.Helper()

	v := types.SanctionVote{
		CaseId:         caseID,
		AgentId:        agent.info.AgentId,
		Option:         types.VoteOption_VOTE_OPTION_APPROVE,
		ApprovedAction: types.SanctionAction_SANCTION_ACTION_BLOCK_TX,
		ReasonCode:     "policy_match",
		SignedHeight:   10,
	}
	sig, err := agent.priv.Sign(keeper.VoteSignBytes("sanction-test-1", v))
	if err != nil {
		t.Fatalf("sign vote: %v", err)
	}
	v.Signature = sig

	resp, err := f.msgServer.SubmitSanctionVote(sdk.WrapSDKContext(f.ctx), &types.MsgSubmitSanctionVote{
		Submitter: agent.info.SignerAddress,
		Vote:      &v,
	})
	if err != nil {
		t.Fatalf("submit vote: %v", err)
	}
	return resp
}
