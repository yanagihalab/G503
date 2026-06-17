package types

import "fmt"

func DefaultParams() Params {
	return Params{
		WatchThreshold:             60,
		BlockThreshold:             80,
		FreezeThreshold:            85,
		RevertThreshold:            90,
		QuorumThreshold:            67,
		UnanimousRequiredForRevert: true,
		EvidenceTtlBlocks:          100,
		VotingPeriodBlocks:         20,
		AcceptedRiskSources:        []string{"chainalysis-mock"},
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
	return &GenesisState{Params: &params}
}

func ValidateGenesis(genesis *GenesisState) error {
	if genesis == nil {
		return fmt.Errorf("genesis state cannot be nil")
	}
	if genesis.Params == nil {
		return fmt.Errorf("params cannot be nil")
	}
	if err := ValidateParams(*genesis.Params); err != nil {
		return err
	}

	seenAgents := map[string]struct{}{}
	seenSigners := map[string]struct{}{}
	for _, agent := range genesis.Agents {
		if agent == nil {
			continue
		}
		if err := ValidateAgent(*agent); err != nil {
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
	}

	return nil
}

func ValidateParams(params Params) error {
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

func ValidateAgent(agent AgentInfo) error {
	if agent.AgentId == "" {
		return fmt.Errorf("agent id cannot be empty")
	}
	if agent.SignerAddress == "" {
		return fmt.Errorf("agent signer address cannot be empty")
	}
	if agent.VotingPower == 0 {
		return fmt.Errorf("agent voting power must be greater than zero")
	}
	return nil
}
