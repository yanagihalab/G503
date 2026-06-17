package keeper

import (
	"context"
	"strconv"

	"cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

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

	if err := m.requireAuthority(msg.Authority); err != nil {
		return nil, err
	}
	if msg.Agent == nil {
		return nil, errors.Wrap(types.ErrDuplicateAgent, "agent is required")
	}
	agent := *msg.Agent
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
	report := *msg.Report
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
	if _, found := m.GetSanctionCase(ctx, msg.CaseId); found {
		return nil, errors.Wrapf(types.ErrDuplicateCase, "case=%s", msg.CaseId)
	}
	if len(msg.ReportIds) == 0 {
		return nil, errors.Wrap(types.ErrRiskReportNotFound, "at least one report id is required")
	}
	if !actionAllowed(msg.RequestedAction, m.GetParams(ctx)) {
		return nil, errors.Wrapf(types.ErrActionNotAllowed, "action=%s", types.ActionName(msg.RequestedAction))
	}

	reports := make([]types.RiskReport, 0, len(msg.ReportIds))
	for _, reportID := range msg.ReportIds {
		report, found := m.GetRiskReport(ctx, reportID)
		if !found {
			return nil, errors.Wrapf(types.ErrRiskReportNotFound, "report=%s", reportID)
		}
		if report.ExpiryHeight <= uint64(ctx.BlockHeight()) {
			return nil, errors.Wrapf(types.ErrRiskReportExpired, "report=%s", reportID)
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

	sanctionCase := types.SanctionCase{
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
	vote := *msg.Vote
	sanctionCase, found := m.GetSanctionCase(ctx, vote.CaseId)
	if !found {
		return nil, types.ErrCaseNotFound
	}
	if sanctionCase.Status != types.CaseStatus_CASE_STATUS_PENDING {
		return nil, errors.Wrapf(types.ErrCaseNotPending, "status=%s", sanctionCase.Status.String())
	}
	if uint64(ctx.BlockHeight()) > sanctionCase.VotingDeadlineHeight {
		sanctionCase.Status = types.CaseStatus_CASE_STATUS_EXPIRED
		m.SetSanctionCase(ctx, sanctionCase)
		return nil, errors.Wrap(types.ErrCaseNotPending, "voting period expired")
	}
	agent, found := m.GetAgent(ctx, vote.AgentId)
	if !found {
		return nil, types.ErrAgentNotFound
	}
	if !agent.Active {
		return nil, types.ErrAgentInactive
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
	approved, rejected := m.recomputeCaseStatus(ctx, &sanctionCase)
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
	if _, found := m.GetExecutionRecord(ctx, msg.CaseId); found {
		return nil, errors.Wrap(types.ErrInvalidCase, "case already executed")
	}
	if sanctionCase.RequestedAction == types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER &&
		m.GetParams(ctx).UnanimousRequiredForRevert &&
		!m.hasUnanimousApproval(ctx, sanctionCase.CaseId) {
		return nil, errors.Wrap(types.ErrQuorumNotReached, "revert requires unanimous approval")
	}

	record := types.ExecutionRecord{
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
		m.SetActiveTxSanction(ctx, types.ActiveSanction{
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
		m.SetFreezeRecord(ctx, types.FreezeRecord{
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

	return &types.MsgExecuteSanctionResponse{Record: &record}, nil
}

func (m msgServer) RevokeSanction(goCtx context.Context, msg *types.MsgRevokeSanction) (*types.MsgRevokeSanctionResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := m.requireAuthority(msg.Authority); err != nil {
		return nil, err
	}
	sanctionCase, found := m.GetSanctionCase(ctx, msg.CaseId)
	if !found {
		return nil, types.ErrCaseNotFound
	}
	if len(sanctionCase.TxHash) > 0 {
		m.DeleteActiveTxSanction(ctx, sanctionCase.TxHash)
	}
	if sanctionCase.TargetAddress != "" {
		m.DeleteFreezeRecord(ctx, sanctionCase.TargetAddress)
	}
	sanctionCase.Status = types.CaseStatus_CASE_STATUS_REVOKED
	m.SetSanctionCase(ctx, sanctionCase)

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		"sanction_revoked",
		sdk.NewAttribute("case_id", msg.CaseId),
		sdk.NewAttribute("reason", msg.Reason),
	))

	return &types.MsgRevokeSanctionResponse{}, nil
}

func (m msgServer) UpdateParams(goCtx context.Context, msg *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := m.requireAuthority(msg.Authority); err != nil {
		return nil, err
	}
	if msg.Params == nil {
		return nil, errors.Wrap(types.ErrInvalidCase, "params are required")
	}
	if err := types.ValidateParams(*msg.Params); err != nil {
		return nil, err
	}
	m.SetParams(ctx, *msg.Params)
	return &types.MsgUpdateParamsResponse{}, nil
}

func (m msgServer) validateRiskReport(ctx sdk.Context, report types.RiskReport) error {
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
	params := m.GetParams(ctx)
	votes := m.GetSanctionVotes(ctx, sanctionCase.CaseId)
	totalPower := uint64(0)
	approvePower := uint64(0)
	rejectPower := uint64(0)

	for _, agent := range m.GetAllAgents(ctx) {
		if agent == nil || !agent.Active {
			continue
		}
		totalPower += agent.VotingPower
		for _, vote := range votes {
			if vote.AgentId != agent.AgentId {
				continue
			}
			switch vote.Option {
			case types.VoteOption_VOTE_OPTION_APPROVE:
				approvePower += agent.VotingPower
			case types.VoteOption_VOTE_OPTION_REJECT:
				rejectPower += agent.VotingPower
			}
			break
		}
	}
	if totalPower == 0 {
		return false, false
	}
	if roundedPercent(approvePower, totalPower) >= uint64(params.QuorumThreshold) {
		sanctionCase.Status = types.CaseStatus_CASE_STATUS_APPROVED
		if sanctionCase.RequestedAction == types.SanctionAction_SANCTION_ACTION_BLOCK_TX {
			m.SetActiveTxSanction(ctx, types.ActiveSanction{
				CaseId:          sanctionCase.CaseId,
				TxHash:          sanctionCase.TxHash,
				TargetAddress:   sanctionCase.TargetAddress,
				Action:          sanctionCase.RequestedAction,
				ActivatedHeight: uint64(ctx.BlockHeight()),
			})
		}
		return true, false
	}
	if roundedPercent(rejectPower, totalPower) >= uint64(params.QuorumThreshold) {
		sanctionCase.Status = types.CaseStatus_CASE_STATUS_REJECTED
		return false, true
	}
	return false, false
}

func roundedPercent(power uint64, totalPower uint64) uint64 {
	if totalPower == 0 {
		return 0
	}
	return (power*100 + totalPower - 1) / totalPower
}

func (m msgServer) hasUnanimousApproval(ctx sdk.Context, caseID string) bool {
	for _, agent := range m.GetAllAgents(ctx) {
		if agent == nil || !agent.Active {
			continue
		}
		vote, found := m.GetSanctionVote(ctx, caseID, agent.AgentId)
		if !found || vote.Option != types.VoteOption_VOTE_OPTION_APPROVE {
			return false
		}
	}
	return true
}

func (m msgServer) requireAuthority(authority string) error {
	if authority != m.GetAuthority() {
		return errors.Wrapf(types.ErrUnauthorized, "invalid authority: got %s want %s", authority, m.GetAuthority())
	}
	return nil
}
