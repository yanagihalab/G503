package types

import (
	"fmt"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/protobuf/proto"
)

func DefaultParams() *Params {
	return &Params{
		WatchThreshold:             60,
		BlockThreshold:             80,
		FreezeThreshold:            85,
		RevertThreshold:            90,
		QuorumThreshold:            67,
		UnanimousRequiredForRevert: true,
		EvidenceTtlBlocks:          100,
		VotingPeriodBlocks:         20,
		AcceptedRiskSources:        []string{"chainalysis-mock", "chainalysis"},
		HighRiskCategories:         []string{"sanctioned", "scam", "darknet", "money_laundering", "terrorist_financing"},
		AllowedActions: []SanctionAction{
			SanctionAction_SANCTION_ACTION_WATCH,
			SanctionAction_SANCTION_ACTION_BLOCK_TX,
			SanctionAction_SANCTION_ACTION_FREEZE_ADDRESS,
			SanctionAction_SANCTION_ACTION_ESCROW_FUNDS,
			SanctionAction_SANCTION_ACTION_REVERT_TRANSFER,
		},
	}
}

func DefaultGenesis() *GenesisState {
	params := DefaultParams()
	return &GenesisState{Params: params}
}

func ValidateGenesis(genesis *GenesisState) error {
	if genesis == nil {
		return fmt.Errorf("genesis state cannot be nil")
	}
	if genesis.Params == nil {
		return fmt.Errorf("params cannot be nil")
	}
	if err := ValidateParams(genesis.Params); err != nil {
		return err
	}

	seenAgents := map[string]struct{}{}
	seenSigners := map[string]struct{}{}
	for _, agent := range genesis.Agents {
		if agent == nil {
			continue
		}
		if err := ValidateAgent(agent); err != nil {
			return err
		}
		if _, found := seenAgents[agent.AgentId]; found {
			return fmt.Errorf("duplicate agent id: %s", agent.AgentId)
		}
		if _, found := seenSigners[agent.SignerAddress]; found {
			return fmt.Errorf("duplicate agent signer: %s", agent.SignerAddress)
		}
		seenAgents[agent.AgentId] = struct{}{}
		seenSigners[agent.SignerAddress] = struct{}{}
	}

	seenReports := map[string]struct{}{}
	for _, report := range genesis.RiskReports {
		if report == nil {
			continue
		}
		if report.ReportId == "" {
			return fmt.Errorf("risk report id cannot be empty")
		}
		if _, found := seenReports[report.ReportId]; found {
			return fmt.Errorf("duplicate risk report id: %s", report.ReportId)
		}
		seenReports[report.ReportId] = struct{}{}
	}

	seenCases := map[string]struct{}{}
	for _, sanctionCase := range genesis.SanctionCases {
		if sanctionCase == nil {
			continue
		}
		if sanctionCase.CaseId == "" {
			return fmt.Errorf("case id cannot be empty")
		}
		if _, found := seenCases[sanctionCase.CaseId]; found {
			return fmt.Errorf("duplicate case id: %s", sanctionCase.CaseId)
		}
		seenCases[sanctionCase.CaseId] = struct{}{}
		if err := ValidateCaseSnapshot(sanctionCase); err != nil {
			return err
		}
	}
	seenLeases := map[string]bool{}
	for _, lease := range genesis.AgentLeases {
		if lease == nil || lease.Epoch == 0 || lease.ExpiresHeight == 0 || len(lease.HolderId) != 64 || strings.Trim(lease.HolderId, "0123456789abcdef") != "" || seenLeases[lease.SignerAddress] {
			return fmt.Errorf("invalid or duplicate agent lease")
		}
		if _, found := seenSigners[lease.SignerAddress]; !found {
			return fmt.Errorf("lease signer is not registered")
		}
		seenLeases[lease.SignerAddress] = true
	}

	return validateSafetyGenesis(genesis)
}

func validateSafetyGenesis(g *GenesisState) error {
	if g.Authority != "" {
		if _, err := sdk.AccAddressFromBech32(g.Authority); err != nil {
			return fmt.Errorf("invalid sanction authority: %w", err)
		}
	}
	key := func(t *OriginalTransfer) string { return fmt.Sprintf("%x/%d/%s", t.TxHash, t.MessageIndex, t.Denom) }
	receipts := map[string]*OriginalTransfer{}
	for _, t := range g.OriginalTransfers {
		if t == nil || len(t.TxHash) != 32 || t.Height == 0 {
			return fmt.Errorf("invalid original transfer")
		}
		if _, err := sdk.AccAddressFromBech32(t.Sender); err != nil {
			return err
		}
		if _, err := sdk.AccAddressFromBech32(t.Recipient); err != nil {
			return err
		}
		coin, err := sdk.ParseCoinNormalized(t.Amount + t.Denom)
		if err != nil || !coin.IsPositive() || coin.Amount.String() != t.Amount || coin.Denom != t.Denom {
			return fmt.Errorf("invalid original transfer coin")
		}
		id := key(t)
		if receipts[id] != nil {
			return fmt.Errorf("duplicate original transfer: %s", id)
		}
		receipts[id] = t
	}
	settled := map[string]bool{}
	for _, r := range g.Settlements {
		if r == nil || r.Transfer == nil || r.CaseId == "" || r.ExecutedHeight <= r.Transfer.Height || (r.Action != SanctionAction_SANCTION_ACTION_REVERT_TRANSFER && r.Action != SanctionAction_SANCTION_ACTION_ESCROW_FUNDS) {
			return fmt.Errorf("invalid settlement")
		}
		id := key(r.Transfer)
		if !proto.Equal(receipts[id], r.Transfer) || settled[id] {
			return fmt.Errorf("duplicate or unbacked settlement: %s", id)
		}
		settled[id] = true
	}
	for _, r := range g.ExecutionRecords {
		if r == nil || (r.Action != SanctionAction_SANCTION_ACTION_REVERT_TRANSFER && r.Action != SanctionAction_SANCTION_ACTION_ESCROW_FUNDS) {
			continue
		}
		found := false
		for _, s := range g.Settlements {
			if s.CaseId == r.CaseId && s.Action == r.Action {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("monetary execution missing settlement: %s", r.CaseId)
		}
	}
	seen := map[string]bool{}
	for _, r := range g.Reviews {
		if r == nil || r.CaseId == "" || len(r.EvidenceHash) != 32 || strings.TrimSpace(r.Reason) == "" || len(r.Reason) > 1024 || r.ReviewedHeight == 0 || (r.RevokedHeight != 0 && r.RevokedHeight < r.ReviewedHeight) || seen[r.CaseId] {
			return fmt.Errorf("invalid or duplicate review")
		}
		if _, err := sdk.AccAddressFromBech32(r.Authority); err != nil {
			return err
		}
		seen[r.CaseId] = true
	}
	return nil
}

func ValidateParams(params *Params) error {
	if params == nil {
		return fmt.Errorf("params cannot be nil")
	}
	if params.WatchThreshold > 100 ||
		params.BlockThreshold > 100 ||
		params.FreezeThreshold > 100 ||
		params.RevertThreshold > 100 {
		return fmt.Errorf("thresholds must be between 0 and 100")
	}
	if params.WatchThreshold > params.BlockThreshold ||
		params.BlockThreshold > params.FreezeThreshold ||
		params.FreezeThreshold > params.RevertThreshold {
		return fmt.Errorf("thresholds must be monotonic: watch <= block <= freeze <= revert")
	}
	if params.QuorumThreshold == 0 || params.QuorumThreshold > 100 {
		return fmt.Errorf("quorum threshold must be between 1 and 100")
	}
	if params.EvidenceTtlBlocks == 0 {
		return fmt.Errorf("evidence ttl blocks must be greater than zero")
	}
	if params.VotingPeriodBlocks == 0 {
		return fmt.Errorf("voting period blocks must be greater than zero")
	}
	if len(params.AcceptedRiskSources) == 0 {
		return fmt.Errorf("at least one accepted risk source is required")
	}
	if len(params.HighRiskCategories) == 0 {
		return fmt.Errorf("at least one high risk category is required")
	}
	if len(params.AllowedActions) == 0 {
		return fmt.Errorf("at least one allowed action is required")
	}
	return nil
}

func ValidateAgent(agent *AgentInfo) error {
	if agent == nil {
		return fmt.Errorf("agent cannot be nil")
	}
	if agent.AgentId == "" {
		return fmt.Errorf("agent id cannot be empty")
	}
	if agent.SignerAddress == "" {
		return fmt.Errorf("agent signer address cannot be empty")
	}
	if agent.Active && agent.VotingPower == 0 {
		return fmt.Errorf("active agent voting power must be greater than zero")
	}
	return nil
}
