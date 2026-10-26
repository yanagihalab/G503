package keeper_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	sdkerrors "cosmossdk.io/errors"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
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

func newFixture(t testing.TB) fixture {
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

func TestFiveAgentThresholdApprovalRequiresFourOfFive(t *testing.T) {
	f := newFixture(t)
	params := f.keeper.GetParams(f.ctx)
	params.QuorumThreshold = 67
	f.keeper.SetParams(f.ctx, params)
	agents := []testAgent{
		registerAgent(t, f, "agent-1"),
		registerAgent(t, f, "agent-2"),
		registerAgent(t, f, "agent-3"),
		registerAgent(t, f, "agent-4"),
		registerAgent(t, f, "agent-5"),
	}
	txHash := bytes.Repeat([]byte{0x5a}, 32)
	submitter := sdk.AccAddress(bytes.Repeat([]byte{0x09}, 20)).String()
	submitReportAndOpenCase(t, f, submitter, "report-five", "case-five", txHash, types.SanctionAction_SANCTION_ACTION_BLOCK_TX)

	for i := 0; i < 3; i++ {
		resp := voteAction(t, f, agents[i], "case-five", types.SanctionAction_SANCTION_ACTION_BLOCK_TX)
		if resp.Approved {
			t.Fatalf("3 of 5 votes should not reach 67 percent quorum")
		}
	}
	if sanctionCase, _ := f.keeper.GetSanctionCase(f.ctx, "case-five"); sanctionCase.Status != types.CaseStatus_CASE_STATUS_PENDING {
		t.Fatalf("expected pending case after 3/5 votes, got %s", sanctionCase.Status)
	}

	resp := voteAction(t, f, agents[3], "case-five", types.SanctionAction_SANCTION_ACTION_BLOCK_TX)
	if !resp.Approved {
		t.Fatalf("4 of 5 votes should reach 67 percent quorum")
	}
	if sanctionCase, _ := f.keeper.GetSanctionCase(f.ctx, "case-five"); sanctionCase.Status != types.CaseStatus_CASE_STATUS_APPROVED {
		t.Fatalf("expected approved case after 4/5 votes, got %s", sanctionCase.Status)
	}
}

func TestRevertTransferRequiresUnanimousFiveAgentApproval(t *testing.T) {
	f := newFixture(t)
	bank := &mockBankKeeper{}
	f.keeper = f.keeper.WithBankKeeper(bank)
	f.msgServer = keeper.NewMsgServerImpl(f.keeper)
	agents := []testAgent{
		registerAgent(t, f, "agent-1"),
		registerAgent(t, f, "agent-2"),
		registerAgent(t, f, "agent-3"),
		registerAgent(t, f, "agent-4"),
		registerAgent(t, f, "agent-5"),
	}
	txHash := bytes.Repeat([]byte{0x6b}, 32)
	submitter := sdk.AccAddress(bytes.Repeat([]byte{0x09}, 20)).String()
	submitReportAndOpenCase(t, f, submitter, "report-revert-five", "case-revert-five", txHash, types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER)

	for i := 0; i < 4; i++ {
		voteAction(t, f, agents[i], "case-revert-five", types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER)
	}
	_, err := f.msgServer.ExecuteSanction(sdk.WrapSDKContext(f.ctx), &types.MsgExecuteSanction{
		Executor: submitter,
		CaseId:   "case-revert-five",
	})
	if !sdkerrors.IsOf(err, types.ErrQuorumNotReached) {
		t.Fatalf("expected unanimous approval error with 4/5 votes, got %v", err)
	}

	voteAction(t, f, agents[4], "case-revert-five", types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER)
	resp, err := f.msgServer.ExecuteSanction(sdk.WrapSDKContext(f.ctx), &types.MsgExecuteSanction{
		Executor: submitter,
		CaseId:   "case-revert-five",
	})
	if err != nil {
		t.Fatalf("execute unanimous revert: %v", err)
	}
	if resp.Record.ResultCode != "ok" || bank.sendCoinsCalls != 1 {
		t.Fatalf("expected revert bank transfer once, record=%+v bank=%+v", resp.Record, bank)
	}
}

func TestVotingDeadlineExpiresPendingCase(t *testing.T) {
	f := newFixture(t)
	agent := registerAgent(t, f, "agent-deadline")
	txHash := bytes.Repeat([]byte{0x7c}, 32)
	submitter := sdk.AccAddress(bytes.Repeat([]byte{0x09}, 20)).String()
	submitReportAndOpenCase(t, f, submitter, "report-deadline", "case-deadline", txHash, types.SanctionAction_SANCTION_ACTION_BLOCK_TX)

	f.ctx = f.ctx.WithBlockHeight(40)
	_, err := f.msgServer.SubmitSanctionVote(sdk.WrapSDKContext(f.ctx), voteMsg(t, f, agent, "case-deadline", types.SanctionAction_SANCTION_ACTION_BLOCK_TX))
	if !sdkerrors.IsOf(err, types.ErrCaseNotPending) {
		t.Fatalf("expected expired voting period error, got %v", err)
	}
	sanctionCase, _ := f.keeper.GetSanctionCase(f.ctx, "case-deadline")
	if sanctionCase.Status != types.CaseStatus_CASE_STATUS_EXPIRED {
		t.Fatalf("expected expired case, got %s", sanctionCase.Status)
	}
}

func TestRevokeSanctionClearsActiveTxAndFreeze(t *testing.T) {
	f := newFixture(t)
	txHash := bytes.Repeat([]byte{0x8d}, 32)
	address := sdk.AccAddress(bytes.Repeat([]byte{0x02}, 20)).String()
	f.keeper.SetSanctionCase(f.ctx, &types.SanctionCase{
		CaseId:        "case-revoke",
		TxHash:        txHash,
		TargetAddress: address,
		Status:        types.CaseStatus_CASE_STATUS_EXECUTED,
	})
	f.keeper.SetActiveTxSanction(f.ctx, &types.ActiveSanction{CaseId: "case-revoke", TxHash: txHash})
	f.keeper.SetFreezeRecord(f.ctx, &types.FreezeRecord{CaseId: "case-revoke", Address: address})
	_, err := f.msgServer.ReviewSanction(f.ctx, &types.MsgReviewSanction{Authority: authority, CaseId: "case-revoke", Reason: "false positive verified", EvidenceHash: bytes.Repeat([]byte{1}, 32)})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.msgServer.RevokeSanction(f.ctx.WithBlockHeight(11), &types.MsgRevokeSanction{
		Authority: f.keeper.GetAuthority(f.ctx),
		CaseId:    "case-revoke",
		Reason:    "false-positive",
	})
	if err != nil {
		t.Fatalf("revoke sanction: %v", err)
	}
	if _, found := f.keeper.GetActiveTxSanction(f.ctx, txHash); found {
		t.Fatalf("active tx sanction should be deleted")
	}
	if _, found := f.keeper.GetFreezeRecord(f.ctx, address); found {
		t.Fatalf("freeze record should be deleted")
	}
	sanctionCase, _ := f.keeper.GetSanctionCase(f.ctx, "case-revoke")
	if sanctionCase.Status != types.CaseStatus_CASE_STATUS_REVOKED {
		t.Fatalf("expected revoked case, got %s", sanctionCase.Status)
	}
}

func TestProcessProposalDeterministicAcrossKeeperInstances(t *testing.T) {
	tx := []byte("settled-sanctioned-tx")
	hash := keeper.TxHash(tx)
	f1 := newFixture(t)
	f2 := newFixture(t)
	sanction := &types.ActiveSanction{
		CaseId: "case-deterministic",
		TxHash: hash,
		Action: types.SanctionAction_SANCTION_ACTION_BLOCK_TX,
	}
	f1.keeper.SetActiveTxSanction(f1.ctx, sanction)
	f2.keeper.SetActiveTxSanction(f2.ctx, sanction)

	found1, ok1 := f1.keeper.ContainsSanctionedTx(f1.ctx, [][]byte{tx})
	found2, ok2 := f2.keeper.ContainsSanctionedTx(f2.ctx, [][]byte{tx})
	if !ok1 || !ok2 || !bytes.Equal(found1.Hash, found2.Hash) {
		t.Fatalf("expected deterministic sanctioned tx detection: %v/%x %v/%x", ok1, found1.Hash, ok2, found2.Hash)
	}
}

func TestSyncAgentRegistryWithValidatorSetUpdatesPowerAndDeactivatesMissingValidators(t *testing.T) {
	f := newFixture(t)
	agent1 := registerAgent(t, f, "agent-sync-1")
	agent2 := registerAgent(t, f, "agent-sync-2")
	agent3 := registerAgent(t, f, "agent-sync-3")
	agent1.info.ValidatorOperatorAddress = "g503valoper1active"
	agent2.info.ValidatorOperatorAddress = "g503valoper1jailed"
	agent3.info.ValidatorOperatorAddress = "g503valoper1missing"
	f.keeper.SetAgent(f.ctx, agent1.info)
	f.keeper.SetAgent(f.ctx, agent2.info)
	f.keeper.SetAgent(f.ctx, agent3.info)

	updated, deactivated := f.keeper.SyncAgentRegistryWithValidatorSet(f.ctx, []keeper.ValidatorAgentState{
		{ValidatorOperatorAddress: "g503valoper1active", VotingPower: 7, Active: true},
		{ValidatorOperatorAddress: "g503valoper1jailed", VotingPower: 0, Active: false},
	})
	if updated != 1 || deactivated != 2 {
		t.Fatalf("unexpected sync counts updated=%d deactivated=%d", updated, deactivated)
	}
	got1, _ := f.keeper.GetAgent(f.ctx, agent1.info.AgentId)
	got2, _ := f.keeper.GetAgent(f.ctx, agent2.info.AgentId)
	got3, _ := f.keeper.GetAgent(f.ctx, agent3.info.AgentId)
	if !got1.Active || got1.VotingPower != 7 {
		t.Fatalf("active validator agent not updated: %+v", got1)
	}
	if got2.Active || got2.VotingPower != 0 {
		t.Fatalf("jailed validator agent not deactivated: %+v", got2)
	}
	if got3.Active || got3.VotingPower != 0 {
		t.Fatalf("missing validator agent not deactivated: %+v", got3)
	}
}

func TestSyncAgentRegistryWithValidatorSetCreatesAgentForNewValidator(t *testing.T) {
	f := newFixture(t)
	key := secp256k1.GenPrivKey()
	updated, deactivated := f.keeper.SyncAgentRegistryWithValidatorSet(f.ctx, []keeper.ValidatorAgentState{
		{
			ValidatorOperatorAddress: "g503valoper1new",
			SignerAddress:            sdk.AccAddress(key.PubKey().Address()).String(),
			PublicKey:                key.PubKey().Bytes(),
			VotingPower:              3,
			Active:                   true,
			MetadataURI:              "staking-sync-test",
		},
	})
	if updated != 1 || deactivated != 0 {
		t.Fatalf("unexpected sync counts updated=%d deactivated=%d", updated, deactivated)
	}
	agent, found := f.keeper.GetAgentByValidatorOperator(f.ctx, "g503valoper1new")
	if !found {
		t.Fatal("expected auto-created validator agent")
	}
	if !agent.Active || agent.VotingPower != 3 || agent.MetadataUri != "staking-sync-test" {
		t.Fatalf("unexpected auto-created agent: %+v", agent)
	}
	setSnapshotCase(t, f, &types.SanctionCase{
		CaseId: "case-auto-agent", RequestedAction: types.SanctionAction_SANCTION_ACTION_WATCH,
		Status: types.CaseStatus_CASE_STATUS_PENDING, VotingDeadlineHeight: 100,
	})
	vote := &types.SanctionVote{CaseId: "case-auto-agent", AgentId: agent.AgentId,
		Option: types.VoteOption_VOTE_OPTION_APPROVE, ApprovedAction: types.SanctionAction_SANCTION_ACTION_WATCH}
	sig, err := key.Sign(keeper.VoteSignBytes("sanction-test-1", vote))
	if err != nil {
		t.Fatal(err)
	}
	vote.Signature = sig
	if _, err := f.msgServer.SubmitSanctionVote(sdk.WrapSDKContext(f.ctx), &types.MsgSubmitSanctionVote{Submitter: agent.SignerAddress, Vote: vote}); err != nil {
		t.Fatalf("auto-created agent cannot vote: %v", err)
	}
}

func TestValidatorWithoutPublicKeyDoesNotGainVotingPower(t *testing.T) {
	f := newFixture(t)
	f.keeper.SyncAgentRegistryWithValidatorSet(f.ctx, []keeper.ValidatorAgentState{{
		ValidatorOperatorAddress: "validator-new", SignerAddress: "signer-new", Active: true, VotingPower: 4,
	}})
	if _, found := f.keeper.GetAgentByValidatorOperator(f.ctx, "validator-new"); found {
		t.Fatal("unusable validator key was admitted to the voting set")
	}
}

func TestProposalFilteringDeterministicWithDifferentMempools(t *testing.T) {
	f := newFixture(t)
	blockedA := []byte("blocked-a")
	blockedB := []byte("blocked-b")
	allowed1 := []byte("allowed-1")
	allowed2 := []byte("allowed-2")
	f.keeper.SetActiveTxSanction(f.ctx, &types.ActiveSanction{CaseId: "case-a", TxHash: keeper.TxHash(blockedA), Action: types.SanctionAction_SANCTION_ACTION_BLOCK_TX})
	f.keeper.SetActiveTxSanction(f.ctx, &types.ActiveSanction{CaseId: "case-b", TxHash: keeper.TxHash(blockedB), Action: types.SanctionAction_SANCTION_ACTION_BLOCK_TX})

	kept1, blocked1 := f.keeper.FilterSanctionedTxs(f.ctx, [][]byte{blockedA, allowed1, blockedB})
	kept2, blocked2 := f.keeper.FilterSanctionedTxs(f.ctx, [][]byte{allowed2, blockedB})
	if len(kept1) != 1 || !bytes.Equal(kept1[0], allowed1) || len(blocked1) != 2 {
		t.Fatalf("unexpected node1 filtering kept=%q blocked=%d", kept1, len(blocked1))
	}
	if len(kept2) != 1 || !bytes.Equal(kept2[0], allowed2) || len(blocked2) != 1 {
		t.Fatalf("unexpected node2 filtering kept=%q blocked=%d", kept2, len(blocked2))
	}
	if !bytes.Equal(blocked1[1].Hash, blocked2[0].Hash) {
		t.Fatalf("same sanctioned tx should hash identically across mempools")
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
	report := &types.RiskReport{
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
		Report:    report,
	})
	if err != nil {
		t.Fatalf("submit signed report: %v", err)
	}

	report.ReportId = "bad-signed-report"
	report.SubmitterSignature = []byte("bad")
	_, err = f.msgServer.SubmitRiskReport(sdk.WrapSDKContext(f.ctx), &types.MsgSubmitRiskReport{
		Submitter: agent.info.SignerAddress,
		Report:    report,
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
	f.keeper.SetActiveTxSanction(f.ctx, &types.ActiveSanction{
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
	f.keeper.SetFreezeRecord(f.ctx, &types.FreezeRecord{
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
	f.keeper.SetOriginalTransfer(f.ctx, &types.OriginalTransfer{TxHash: bytes.Repeat([]byte{0xee}, 32), Sender: sender, Recipient: recipient, Amount: "7", Denom: "utoken", Height: 9})
	f.keeper.SetOriginalTransfer(f.ctx, &types.OriginalTransfer{TxHash: bytes.Repeat([]byte{0xdd}, 32), Sender: sender, Recipient: recipient, Amount: "5", Denom: "utoken", Height: 9})

	f.keeper.SetRiskReport(f.ctx, &types.RiskReport{
		ReportId:  "report-escrow",
		TxHash:    bytes.Repeat([]byte{0xee}, 32),
		Sender:    sender,
		Recipient: recipient,
		Amount:    "7",
		Denom:     "utoken",
		Source:    "chainalysis-mock",
	})
	setSnapshotCase(t, f, &types.SanctionCase{
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

	f.keeper.SetRiskReport(f.ctx, &types.RiskReport{
		ReportId:  "report-revert",
		TxHash:    bytes.Repeat([]byte{0xdd}, 32),
		Sender:    sender,
		Recipient: recipient,
		Amount:    "5",
		Denom:     "utoken",
		Source:    "chainalysis-mock",
	})
	params := f.keeper.GetParams(f.ctx)
	params.UnanimousRequiredForRevert = false
	f.keeper.SetParams(f.ctx, params)
	setSnapshotCase(t, f, &types.SanctionCase{
		CaseId:          "case-revert",
		TxHash:          bytes.Repeat([]byte{0xdd}, 32),
		RequestedAction: types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER,
		Status:          types.CaseStatus_CASE_STATUS_APPROVED,
		ReportIds:       []string{"report-revert"},
		TargetAddress:   recipient,
	})

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
	info *types.AgentInfo
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
		info: &types.AgentInfo{
			AgentId:       agentID,
			SignerAddress: signer,
			PublicKey:     priv.PubKey().Bytes(),
			VotingPower:   1,
			Active:        true,
		},
	}
	_, err := f.msgServer.RegisterAgent(sdk.WrapSDKContext(f.ctx), &types.MsgRegisterAgent{
		Authority: f.keeper.GetAuthority(f.ctx),
		Agent:     agent.info,
	})
	if err != nil {
		t.Fatalf("register agent %s: %v", agentID, err)
	}
	return agent
}

func vote(t *testing.T, f fixture, agent testAgent, caseID string) *types.MsgSubmitSanctionVoteResponse {
	t.Helper()

	resp, err := f.msgServer.SubmitSanctionVote(sdk.WrapSDKContext(f.ctx), voteMsg(t, f, agent, caseID, types.SanctionAction_SANCTION_ACTION_BLOCK_TX))
	if err != nil {
		t.Fatalf("submit vote: %v", err)
	}
	return resp
}

func voteAction(t *testing.T, f fixture, agent testAgent, caseID string, action types.SanctionAction) *types.MsgSubmitSanctionVoteResponse {
	t.Helper()

	resp, err := f.msgServer.SubmitSanctionVote(sdk.WrapSDKContext(f.ctx), voteMsg(t, f, agent, caseID, action))
	if err != nil {
		t.Fatalf("submit vote: %v", err)
	}
	return resp
}

func voteMsg(t *testing.T, f fixture, agent testAgent, caseID string, action types.SanctionAction) *types.MsgSubmitSanctionVote {
	t.Helper()

	v := &types.SanctionVote{
		CaseId:         caseID,
		AgentId:        agent.info.AgentId,
		Option:         types.VoteOption_VOTE_OPTION_APPROVE,
		ApprovedAction: action,
		ReasonCode:     "policy_match",
		SignedHeight:   10,
	}
	sig, err := agent.priv.Sign(keeper.VoteSignBytes("sanction-test-1", v))
	if err != nil {
		t.Fatalf("sign vote: %v", err)
	}
	v.Signature = sig

	return &types.MsgSubmitSanctionVote{
		Submitter: agent.info.SignerAddress,
		Vote:      v,
	}
}

func submitReportAndOpenCase(t *testing.T, f fixture, submitter string, reportID string, caseID string, txHash []byte, action types.SanctionAction) {
	t.Helper()

	riskScore := uint32(86)
	if action == types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER {
		riskScore = 95
	}
	sender := sdk.AccAddress(bytes.Repeat([]byte{0x01}, 20)).String()
	recipient := sdk.AccAddress(bytes.Repeat([]byte{0x02}, 20)).String()
	if action == types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER || action == types.SanctionAction_SANCTION_ACTION_ESCROW_FUNDS {
		f.keeper.SetOriginalTransfer(f.ctx, &types.OriginalTransfer{TxHash: txHash, Sender: sender, Recipient: recipient, Amount: "5", Denom: "utoken", Height: 9})
	}
	_, err := f.msgServer.SubmitRiskReport(sdk.WrapSDKContext(f.ctx), &types.MsgSubmitRiskReport{
		Submitter: submitter,
		Report: &types.RiskReport{
			ReportId:       reportID,
			TxHash:         txHash,
			Sender:         sender,
			Recipient:      recipient,
			Denom:          "utoken",
			Amount:         "5",
			Source:         "chainalysis-mock",
			RiskScore:      riskScore,
			RiskCategories: []string{"money_laundering"},
			ObservedHeight: 10,
			ExpiryHeight:   40,
			PolicyVersion:  "poc-v1",
			EvidenceHash:   []byte("evidence"),
			Submitter:      submitter,
		},
	})
	if err != nil {
		t.Fatalf("submit risk report: %v", err)
	}
	targetType := types.SanctionTargetType_SANCTION_TARGET_TYPE_TX
	if action == types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER ||
		action == types.SanctionAction_SANCTION_ACTION_FREEZE_ADDRESS ||
		action == types.SanctionAction_SANCTION_ACTION_ESCROW_FUNDS {
		targetType = types.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS
	}
	_, err = f.msgServer.OpenSanctionCase(sdk.WrapSDKContext(f.ctx), &types.MsgOpenSanctionCase{
		Submitter:       submitter,
		CaseId:          caseID,
		TxHash:          txHash,
		TargetAddress:   recipient,
		TargetType:      targetType,
		RequestedAction: action,
		ReportIds:       []string{reportID},
		PolicyVersion:   "poc-v1",
	})
	if err != nil {
		t.Fatalf("open sanction case: %v", err)
	}
}
