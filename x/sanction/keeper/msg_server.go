package keeper

import (
	"bytes"
	"context"
	"math/big"
	"strconv"
	"strings"

	"cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/protobuf/proto"

	"github.com/yanagihalab/G503/x/sanction/types"
)

type msgServer struct {
	Keeper
}

var _ types.MsgServer = msgServer{}

func NewMsgServerImpl(k Keeper) types.MsgServer {
	return msgServer{Keeper: k}
}

func (m msgServer) RegisterAgent(goCtx context.Context, msg *types.MsgRegisterAgent) (*types.MsgRegisterAgentResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := m.requireAuthority(ctx, msg.Authority); err != nil {
		return nil, err
	}
	if msg.Agent == nil {
		return nil, errors.Wrap(types.ErrDuplicateAgent, "agent is required")
	}
	agent := proto.Clone(msg.Agent).(*types.AgentInfo)
	if err := types.ValidateAgent(agent); err != nil {
		return nil, errors.Wrap(types.ErrDuplicateAgent, err.Error())
	}
	if _, err := sdk.AccAddressFromBech32(agent.SignerAddress); err != nil {
		return nil, err
	}
	if _, found := m.GetAgent(ctx, agent.AgentId); found {
		return nil, errors.Wrapf(types.ErrDuplicateAgent, "agent=%s", agent.AgentId)
	}
	if _, found := m.GetAgentBySigner(ctx, agent.SignerAddress); found {
		return nil, errors.Wrapf(types.ErrDuplicateAgent, "signer=%s", agent.SignerAddress)
	}

	m.SetAgent(ctx, agent)
	return &types.MsgRegisterAgentResponse{}, nil
}

func (m msgServer) SubmitRiskReport(goCtx context.Context, msg *types.MsgSubmitRiskReport) (*types.MsgSubmitRiskReportResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if _, err := sdk.AccAddressFromBech32(msg.Submitter); err != nil {
		return nil, err
	}
	if msg.Report == nil {
		return nil, errors.Wrap(types.ErrRiskReportNotFound, "report is required")
	}
	report := proto.Clone(msg.Report).(*types.RiskReport)
	if report.Submitter == "" {
		report.Submitter = msg.Submitter
	}
	if report.Submitter != msg.Submitter {
		return nil, errors.Wrap(types.ErrUnauthorized, "message submitter does not match report submitter")
	}
	if err := m.validateRiskReport(ctx, report); err != nil {
		return nil, err
	}
	if _, found := m.GetRiskReport(ctx, report.ReportId); found {
		return nil, errors.Wrapf(types.ErrDuplicateCase, "risk report=%s", report.ReportId)
	}
	if agent, found := m.GetAgentBySigner(ctx, report.Submitter); found && len(report.SubmitterSignature) > 0 {
		if !VerifySecp256k1(agent.PublicKey, report.SubmitterSignature, RiskReportSignBytes(m.GetChainID(ctx), report)) {
			return nil, errors.Wrap(types.ErrInvalidSignature, "risk report signature verification failed")
		}
	}

	m.SetRiskReport(ctx, report)
	ctx.EventManager().EmitEvent(sdk.NewEvent(
		"risk_report_submitted",
		sdk.NewAttribute("report_id", report.ReportId),
		sdk.NewAttribute("source", report.Source),
		sdk.NewAttribute("risk_score", strconv.FormatUint(uint64(report.RiskScore), 10)),
	))

	return &types.MsgSubmitRiskReportResponse{ReportId: report.ReportId}, nil
}

func (m msgServer) OpenSanctionCase(goCtx context.Context, msg *types.MsgOpenSanctionCase) (*types.MsgOpenSanctionCaseResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if _, err := sdk.AccAddressFromBech32(msg.Submitter); err != nil {
		return nil, err
	}
	if msg.CaseId == "" {
		return nil, errors.Wrap(types.ErrInvalidCase, "case id is required")
	}
	if len(msg.TxHash) != 32 || (msg.TargetType != types.SanctionTargetType_SANCTION_TARGET_TYPE_TX && msg.TargetType != types.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS) {
		return nil, errors.Wrap(types.ErrInvalidCase, "32-byte tx hash and valid target type are required")
	}
	if msg.TargetType == types.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS && msg.TargetAddress == "" {
		return nil, errors.Wrap(types.ErrInvalidCase, "address target requires a recipient")
	}
	if msg.TargetAddress != "" {
		if _, err := sdk.AccAddressFromBech32(msg.TargetAddress); err != nil {
			return nil, err
		}
	}
	if _, found := m.GetSanctionCase(ctx, msg.CaseId); found {
		return nil, errors.Wrapf(types.ErrDuplicateCase, "case=%s", msg.CaseId)
	}
	if len(msg.ReportIds) == 0 {
		return nil, errors.Wrap(types.ErrRiskReportNotFound, "at least one report id is required")
	}
	if !actionAllowed(msg.RequestedAction, m.GetParams(ctx)) {
		return nil, errors.Wrapf(types.ErrActionNotAllowed, "action=%s", types.ActionName(msg.RequestedAction))
	}

	reports := make([]*types.RiskReport, 0, len(msg.ReportIds))
	for _, reportID := range msg.ReportIds {
		report, found := m.GetRiskReport(ctx, reportID)
		if !found {
			return nil, errors.Wrapf(types.ErrRiskReportNotFound, "report=%s", reportID)
		}
		if report.ExpiryHeight <= uint64(ctx.BlockHeight()) {
			return nil, errors.Wrapf(types.ErrRiskReportExpired, "report=%s", reportID)
		}
		if !bytes.Equal(report.TxHash, msg.TxHash) || (msg.TargetAddress != "" && report.Recipient != msg.TargetAddress) {
			return nil, errors.Wrap(types.ErrInvalidCase, "risk report does not match case tx or recipient")
		}
		if len(reports) > 0 {
			primary := reports[0]
			if report.MessageIndex != primary.MessageIndex || report.Sender != primary.Sender || report.Recipient != primary.Recipient || report.Amount != primary.Amount || report.Denom != primary.Denom {
				return nil, errors.Wrap(types.ErrInvalidCase, "risk reports describe different transfers")
			}
		}
		if msg.RequestedAction == types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER || msg.RequestedAction == types.SanctionAction_SANCTION_ACTION_ESCROW_FUNDS {
			if _, err := m.unsettledOriginal(ctx, report); err != nil {
				return nil, err
			}
			if _, err := reportCoins(report); err != nil {
				return nil, errors.Wrap(types.ErrInvalidCase, err.Error())
			}
			if _, err := sdk.AccAddressFromBech32(report.Recipient); err != nil {
				return nil, err
			}
			if msg.RequestedAction == types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER {
				if _, err := sdk.AccAddressFromBech32(report.Sender); err != nil {
					return nil, err
				}
			}
		}
		reports = append(reports, report)
	}

	params := m.GetParams(ctx)
	expectedAction := ComputePolicyAction(reports, params, msg.TargetType)
	if expectedAction == types.SanctionAction_SANCTION_ACTION_UNSPECIFIED {
		return nil, errors.Wrap(types.ErrActionNotAllowed, "policy did not recommend a sanction")
	}
	if msg.RequestedAction != expectedAction {
		return nil, errors.Wrapf(types.ErrActionNotAllowed, "requested=%s expected=%s", types.ActionName(msg.RequestedAction), types.ActionName(expectedAction))
	}
	if msg.RequestedAction == types.SanctionAction_SANCTION_ACTION_BLOCK_TX {
		if _, found := m.GetActiveTxSanction(ctx, msg.TxHash); found {
			return nil, errors.Wrap(types.ErrTxAlreadySanctioned, "tx already has active sanction")
		}
	}
	if msg.TargetAddress != "" && msg.RequestedAction == types.SanctionAction_SANCTION_ACTION_FREEZE_ADDRESS {
		if _, found := m.GetFreezeRecord(ctx, msg.TargetAddress); found {
			return nil, errors.Wrap(types.ErrAddressAlreadyFrozen, "address already frozen")
		}
	}
	if msg.PreviousCaseId != "" {
		if err := m.expireForReplacement(ctx, msg.PreviousCaseId, reports[0], msg.TargetType); err != nil {
			return nil, err
		}
	}

	sanctionCase := &types.SanctionCase{
		PreviousCaseId:       msg.PreviousCaseId,
		CaseId:               msg.CaseId,
		TxHash:               msg.TxHash,
		TargetAddress:        msg.TargetAddress,
		TargetType:           msg.TargetType,
		RequestedAction:      msg.RequestedAction,
		Status:               types.CaseStatus_CASE_STATUS_PENDING,
		ReportIds:            msg.ReportIds,
		OpenedHeight:         uint64(ctx.BlockHeight()),
		VotingDeadlineHeight: uint64(ctx.BlockHeight()) + params.VotingPeriodBlocks,
		PolicyVersion:        msg.PolicyVersion,
	}
	if err := m.SnapshotCaseVoters(ctx, sanctionCase); err != nil {
		return nil, err
	}
	sanctionCase.DecisionHash = DecisionHash(sanctionCase)
	m.SetSanctionCase(ctx, sanctionCase)

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		"sanction_case_opened",
		sdk.NewAttribute("case_id", sanctionCase.CaseId),
		sdk.NewAttribute("action", types.ActionName(sanctionCase.RequestedAction)),
	))

	return &types.MsgOpenSanctionCaseResponse{CaseId: sanctionCase.CaseId}, nil
}

func (m msgServer) SubmitSanctionVote(goCtx context.Context, msg *types.MsgSubmitSanctionVote) (*types.MsgSubmitSanctionVoteResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if _, err := sdk.AccAddressFromBech32(msg.Submitter); err != nil {
		return nil, err
	}
	if msg.Vote == nil {
		return nil, errors.Wrap(types.ErrInvalidCase, "vote is required")
	}
	vote := proto.Clone(msg.Vote).(*types.SanctionVote)
	if vote.Option != types.VoteOption_VOTE_OPTION_APPROVE && vote.Option != types.VoteOption_VOTE_OPTION_REJECT && vote.Option != types.VoteOption_VOTE_OPTION_ABSTAIN {
		return nil, errors.Wrap(types.ErrInvalidCase, "invalid vote option")
	}
	sanctionCase, found := m.GetSanctionCase(ctx, vote.CaseId)
	if !found {
		return nil, types.ErrCaseNotFound
	}
	allowPostApprovalVote := sanctionCase.Status == types.CaseStatus_CASE_STATUS_APPROVED &&
		sanctionCase.RequestedAction == types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER
	if sanctionCase.Status != types.CaseStatus_CASE_STATUS_PENDING && !allowPostApprovalVote {
		return nil, errors.Wrapf(types.ErrCaseNotPending, "status=%s", sanctionCase.Status.String())
	}
	if sanctionCase.Status == types.CaseStatus_CASE_STATUS_PENDING && uint64(ctx.BlockHeight()) > sanctionCase.VotingDeadlineHeight {
		sanctionCase.Status = types.CaseStatus_CASE_STATUS_EXPIRED
		m.SetSanctionCase(ctx, sanctionCase)
		return nil, errors.Wrap(types.ErrCaseNotPending, "voting period expired")
	}
	agent, found := caseVoter(sanctionCase, vote.AgentId)
	if !found {
		return nil, types.ErrAgentNotFound
	}
	if agent.SignerAddress != msg.Submitter {
		return nil, errors.Wrap(types.ErrUnauthorized, "vote submitter does not match agent signer")
	}
	if _, found := m.GetSanctionVote(ctx, vote.CaseId, vote.AgentId); found {
		return nil, types.ErrVoteAlreadySubmitted
	}
	if vote.ApprovedAction != sanctionCase.RequestedAction && vote.Option == types.VoteOption_VOTE_OPTION_APPROVE {
		return nil, errors.Wrap(types.ErrActionNotAllowed, "approved action does not match case action")
	}
	if !VerifySecp256k1(agent.PublicKey, vote.Signature, VoteSignBytes(m.GetChainID(ctx), vote)) {
		return nil, errors.Wrap(types.ErrInvalidSignature, "vote signature verification failed")
	}

	m.SetSanctionVote(ctx, vote)
	approved, rejected := m.recomputeCaseStatus(ctx, sanctionCase)
	m.SetSanctionCase(ctx, sanctionCase)

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		"sanction_vote_submitted",
		sdk.NewAttribute("case_id", vote.CaseId),
		sdk.NewAttribute("agent_id", vote.AgentId),
		sdk.NewAttribute("option", vote.Option.String()),
	))

	return &types.MsgSubmitSanctionVoteResponse{Approved: approved, Rejected: rejected}, nil
}

func (m msgServer) ExecuteSanction(goCtx context.Context, msg *types.MsgExecuteSanction) (*types.MsgExecuteSanctionResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if _, err := sdk.AccAddressFromBech32(msg.Executor); err != nil {
		return nil, err
	}
	sanctionCase, found := m.GetSanctionCase(ctx, msg.CaseId)
	if !found {
		return nil, types.ErrCaseNotFound
	}
	if sanctionCase.Status != types.CaseStatus_CASE_STATUS_APPROVED {
		return nil, errors.Wrapf(types.ErrQuorumNotReached, "case status=%s", sanctionCase.Status.String())
	}
	if err := types.ValidateCaseSnapshot(sanctionCase); err != nil {
		return nil, err
	}
	if _, found := m.GetExecutionRecord(ctx, msg.CaseId); found {
		return nil, errors.Wrap(types.ErrInvalidCase, "case already executed")
	}
	if sanctionCase.RequestedAction == types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER &&
		sanctionCase.UnanimousRequiredForRevert &&
		!m.hasUnanimousApproval(ctx, sanctionCase.CaseId) {
		return nil, errors.Wrap(types.ErrQuorumNotReached, "revert requires unanimous approval")
	}
	if len(sanctionCase.ReportIds) > 0 {
		report, found := m.GetRiskReport(ctx, sanctionCase.ReportIds[0])
		if !found {
			return nil, types.ErrRiskReportNotFound
		}
		if report.ExpiryHeight > 0 && report.ExpiryHeight <= uint64(ctx.BlockHeight()) {
			return nil, types.ErrRiskReportExpired
		}
	}

	record := &types.ExecutionRecord{
		CaseId:         sanctionCase.CaseId,
		Action:         sanctionCase.RequestedAction,
		TxHash:         sanctionCase.TxHash,
		TargetAddress:  sanctionCase.TargetAddress,
		Executor:       msg.Executor,
		ExecutedHeight: uint64(ctx.BlockHeight()),
		ResultCode:     "ok",
		ResultMessage:  msg.ResultMessage,
	}

	switch sanctionCase.RequestedAction {
	case types.SanctionAction_SANCTION_ACTION_BLOCK_TX:
		m.SetActiveTxSanction(ctx, &types.ActiveSanction{
			CaseId:          sanctionCase.CaseId,
			TxHash:          sanctionCase.TxHash,
			TargetAddress:   sanctionCase.TargetAddress,
			Action:          sanctionCase.RequestedAction,
			ActivatedHeight: uint64(ctx.BlockHeight()),
		})
	case types.SanctionAction_SANCTION_ACTION_FREEZE_ADDRESS:
		if sanctionCase.TargetAddress == "" {
			return nil, errors.Wrap(types.ErrInvalidCase, "target address is required for freeze")
		}
		m.SetFreezeRecord(ctx, &types.FreezeRecord{
			CaseId:       sanctionCase.CaseId,
			Address:      sanctionCase.TargetAddress,
			FrozenHeight: uint64(ctx.BlockHeight()),
		})
	case types.SanctionAction_SANCTION_ACTION_WATCH:
		record.ResultCode = "watch_only"
	case types.SanctionAction_SANCTION_ACTION_ESCROW_FUNDS:
		stateHash, err := m.executeEscrow(ctx, sanctionCase)
		if err != nil {
			return nil, err
		}
		record.StateChangeHash = stateHash
	case types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER:
		stateHash, err := m.executeRevert(ctx, sanctionCase)
		if err != nil {
			return nil, err
		}
		record.StateChangeHash = stateHash
	default:
		return nil, errors.Wrap(types.ErrActionNotAllowed, "unsupported action")
	}

	sanctionCase.Status = types.CaseStatus_CASE_STATUS_EXECUTED
	sanctionCase.ExecutedHeight = uint64(ctx.BlockHeight())
	m.SetSanctionCase(ctx, sanctionCase)
	m.SetExecutionRecord(ctx, record)

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		"sanction_executed",
		sdk.NewAttribute("case_id", record.CaseId),
		sdk.NewAttribute("action", types.ActionName(record.Action)),
		sdk.NewAttribute("result_code", record.ResultCode),
	))

	return &types.MsgExecuteSanctionResponse{Record: record}, nil
}

func (m msgServer) RevokeSanction(goCtx context.Context, msg *types.MsgRevokeSanction) (*types.MsgRevokeSanctionResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := m.requireAuthority(ctx, msg.Authority); err != nil {
		return nil, err
	}
	sanctionCase, found := m.GetSanctionCase(ctx, msg.CaseId)
	if !found {
		return nil, types.ErrCaseNotFound
	}
	review, reviewed := m.GetReview(ctx, msg.CaseId)
	if !reviewed || review.Authority != msg.Authority || review.ReviewedHeight >= uint64(ctx.BlockHeight()) || review.RevokedHeight != 0 || sanctionCase.Status == types.CaseStatus_CASE_STATUS_REVOKED {
		return nil, types.ErrReviewRequired
	}
	if strings.TrimSpace(msg.Reason) == "" || len(msg.Reason) > 1024 {
		return nil, errors.Wrap(types.ErrInvalidCase, "revoke reason must contain 1-1024 bytes")
	}
	if active, found := m.GetActiveTxSanction(ctx, sanctionCase.TxHash); found && active.CaseId == msg.CaseId {
		m.DeleteActiveTxSanction(ctx, sanctionCase.TxHash)
	}
	if freeze, found := m.GetFreezeRecord(ctx, sanctionCase.TargetAddress); found && freeze.CaseId == msg.CaseId {
		m.DeleteFreezeRecord(ctx, sanctionCase.TargetAddress)
	}
	sanctionCase.Status = types.CaseStatus_CASE_STATUS_REVOKED
	m.SetSanctionCase(ctx, sanctionCase)
	review.RevokedHeight = uint64(ctx.BlockHeight())
	review.RevokeReason = msg.Reason
	m.SetReview(ctx, review)

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		"sanction_revoked",
		sdk.NewAttribute("case_id", msg.CaseId),
		sdk.NewAttribute("reason", msg.Reason),
	))

	return &types.MsgRevokeSanctionResponse{}, nil
}

func (m msgServer) ReviewSanction(goCtx context.Context, msg *types.MsgReviewSanction) (*types.MsgReviewSanctionResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	if err := m.requireAuthority(ctx, msg.Authority); err != nil {
		return nil, err
	}
	c, found := m.GetSanctionCase(ctx, msg.CaseId)
	if !found {
		return nil, types.ErrCaseNotFound
	}
	if c.Status == types.CaseStatus_CASE_STATUS_REVOKED {
		return nil, types.ErrCaseNotPending
	}
	if _, found := m.GetReview(ctx, msg.CaseId); found {
		return nil, errors.Wrap(types.ErrInvalidCase, "accepted review is immutable")
	}
	if len(msg.EvidenceHash) != 32 || strings.TrimSpace(msg.Reason) == "" || len(msg.Reason) > 1024 {
		return nil, errors.Wrap(types.ErrInvalidCase, "32-byte evidence hash and 1-1024 byte review reason required")
	}
	m.SetReview(ctx, &types.ReviewRecord{CaseId: msg.CaseId, Authority: msg.Authority, Reason: msg.Reason, EvidenceHash: msg.EvidenceHash, ReviewedHeight: uint64(ctx.BlockHeight())})
	ctx.EventManager().EmitEvent(sdk.NewEvent("sanction_reviewed", sdk.NewAttribute("case_id", msg.CaseId), sdk.NewAttribute("authority", msg.Authority)))
	return &types.MsgReviewSanctionResponse{}, nil
}

func (m msgServer) UpdateParams(goCtx context.Context, msg *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := m.requireAuthority(ctx, msg.Authority); err != nil {
		return nil, err
	}
	if msg.Params == nil {
		return nil, errors.Wrap(types.ErrInvalidCase, "params are required")
	}
	if err := types.ValidateParams(msg.Params); err != nil {
		return nil, err
	}
	m.SetParams(ctx, msg.Params)
	return &types.MsgUpdateParamsResponse{}, nil
}

func (m msgServer) validateRiskReport(ctx sdk.Context, report *types.RiskReport) error {
	if report.ReportId == "" {
		return errors.Wrap(types.ErrRiskReportNotFound, "report id is required")
	}
	if len(report.TxHash) == 0 {
		return errors.Wrap(types.ErrInvalidCase, "tx hash is required")
	}
	if report.RiskScore > 100 {
		return types.ErrInvalidRiskScore
	}
	if len(report.EvidenceHash) == 0 {
		return errors.Wrap(types.ErrInvalidCase, "evidence hash is required")
	}
	if report.ExpiryHeight <= uint64(ctx.BlockHeight()) {
		return types.ErrRiskReportExpired
	}
	if !stringInSlice(report.Source, m.GetParams(ctx).AcceptedRiskSources) {
		return errors.Wrapf(types.ErrSourceNotAccepted, "source=%s", report.Source)
	}
	return nil
}

func (m msgServer) recomputeCaseStatus(ctx sdk.Context, sanctionCase *types.SanctionCase) (bool, bool) {
	if sanctionCase.VotingSnapshotVersion != 1 {
		return false, false
	}
	votes := m.GetSanctionVotes(ctx, sanctionCase.CaseId)
	tally := tallySnapshotVotes(sanctionCase.Voters, votes)
	if tally.total.Sign() == 0 {
		return false, false
	}
	if roundedPercent(&tally.approve, &tally.total) >= uint64(sanctionCase.QuorumThreshold) {
		sanctionCase.Status = types.CaseStatus_CASE_STATUS_APPROVED
		if sanctionCase.RequestedAction == types.SanctionAction_SANCTION_ACTION_BLOCK_TX {
			m.SetActiveTxSanction(ctx, &types.ActiveSanction{
				CaseId:          sanctionCase.CaseId,
				TxHash:          sanctionCase.TxHash,
				TargetAddress:   sanctionCase.TargetAddress,
				Action:          sanctionCase.RequestedAction,
				ActivatedHeight: uint64(ctx.BlockHeight()),
			})
		}
		return true, false
	}
	if roundedPercent(&tally.reject, &tally.total) >= uint64(sanctionCase.QuorumThreshold) {
		sanctionCase.Status = types.CaseStatus_CASE_STATUS_REJECTED
		return false, true
	}
	return false, false
}

func roundedPercent(power, totalPower *big.Int) uint64 {
	if totalPower.Sign() == 0 {
		return 0
	}
	numerator := new(big.Int).Mul(power, big.NewInt(100))
	numerator.Add(numerator, new(big.Int).Sub(totalPower, big.NewInt(1)))
	return numerator.Quo(numerator, totalPower).Uint64()
}

func (m msgServer) hasUnanimousApproval(ctx sdk.Context, caseID string) bool {
	c, found := m.GetSanctionCase(ctx, caseID)
	if !found || c.VotingSnapshotVersion != 1 {
		return false
	}
	active := 0
	for _, agent := range c.Voters {
		if agent == nil || !agent.Active || agent.VotingPower == 0 {
			continue
		}
		active++
		vote, found := m.GetSanctionVote(ctx, caseID, agent.AgentId)
		if !found || vote.Option != types.VoteOption_VOTE_OPTION_APPROVE {
			return false
		}
	}
	return active > 0
}

func (m msgServer) requireAuthority(ctx sdk.Context, authority string) error {
	if authority != m.GetAuthority(ctx) {
		return errors.Wrapf(types.ErrUnauthorized, "invalid authority: got %s want %s", authority, m.GetAuthority(ctx))
	}
	return nil
}
