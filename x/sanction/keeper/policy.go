package keeper

import (
	"crypto/sha256"

	"github.com/yanagihalab/G503/x/sanction/types"
)

func ComputePolicyAction(reports []types.RiskReport, params types.Params, targetType types.SanctionTargetType) types.SanctionAction {
	maxScore := uint32(0)
	highRisk := false
	for _, report := range reports {
		if report.RiskScore > maxScore {
			maxScore = report.RiskScore
		}
		for _, category := range report.RiskCategories {
			if stringInSlice(category, params.HighRiskCategories) {
				highRisk = true
			}
		}
	}

	if !highRisk {
		if maxScore >= params.WatchThreshold {
			return types.SanctionAction_SANCTION_ACTION_WATCH
		}
		return types.SanctionAction_SANCTION_ACTION_UNSPECIFIED
	}

	action := types.SanctionAction_SANCTION_ACTION_UNSPECIFIED
	switch {
	case maxScore >= params.RevertThreshold:
		action = types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER
	case maxScore >= params.FreezeThreshold:
		action = types.SanctionAction_SANCTION_ACTION_FREEZE_ADDRESS
	case maxScore >= params.BlockThreshold:
		action = types.SanctionAction_SANCTION_ACTION_BLOCK_TX
	case maxScore >= params.WatchThreshold:
		action = types.SanctionAction_SANCTION_ACTION_WATCH
	}

	if targetType == types.SanctionTargetType_SANCTION_TARGET_TYPE_TX &&
		(action == types.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER ||
			action == types.SanctionAction_SANCTION_ACTION_FREEZE_ADDRESS ||
			action == types.SanctionAction_SANCTION_ACTION_ESCROW_FUNDS) {
		return types.SanctionAction_SANCTION_ACTION_BLOCK_TX
	}

	return action
}

func DecisionHash(sanctionCase types.SanctionCase) []byte {
	copyCase := sanctionCase
	copyCase.DecisionHash = nil
	sum := sha256.Sum256(types.ModuleCdc.MustMarshal(&copyCase))
	return sum[:]
}

func stringInSlice(value string, values []string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func actionAllowed(action types.SanctionAction, params types.Params) bool {
	for _, allowed := range params.AllowedActions {
		if action == allowed {
			return true
		}
	}
	return false
}
