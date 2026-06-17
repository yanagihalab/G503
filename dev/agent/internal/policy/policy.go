package policy

import sanctiontypes "github.com/yanagihalab/G503/x/sanction/types"

type Thresholds struct {
	WatchThreshold     uint32
	BlockThreshold     uint32
	FreezeThreshold    uint32
	RevertThreshold    uint32
	HighRiskCategories []string
}

func DefaultThresholds() Thresholds {
	params := sanctiontypes.DefaultParams()
	return Thresholds{
		WatchThreshold:     params.WatchThreshold,
		BlockThreshold:     params.BlockThreshold,
		FreezeThreshold:    params.FreezeThreshold,
		RevertThreshold:    params.RevertThreshold,
		HighRiskCategories: params.HighRiskCategories,
	}
}

func Action(score uint32, categories []string, targetType sanctiontypes.SanctionTargetType, thresholds Thresholds) sanctiontypes.SanctionAction {
	highRisk := false
	for _, category := range categories {
		for _, highRiskCategory := range thresholds.HighRiskCategories {
			if category == highRiskCategory {
				highRisk = true
				break
			}
		}
	}
	if !highRisk {
		if score >= thresholds.WatchThreshold {
			return sanctiontypes.SanctionAction_SANCTION_ACTION_WATCH
		}
		return sanctiontypes.SanctionAction_SANCTION_ACTION_UNSPECIFIED
	}

	action := sanctiontypes.SanctionAction_SANCTION_ACTION_UNSPECIFIED
	switch {
	case score >= thresholds.RevertThreshold:
		action = sanctiontypes.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER
	case score >= thresholds.FreezeThreshold:
		action = sanctiontypes.SanctionAction_SANCTION_ACTION_FREEZE_ADDRESS
	case score >= thresholds.BlockThreshold:
		action = sanctiontypes.SanctionAction_SANCTION_ACTION_BLOCK_TX
	case score >= thresholds.WatchThreshold:
		action = sanctiontypes.SanctionAction_SANCTION_ACTION_WATCH
	}

	if targetType == sanctiontypes.SanctionTargetType_SANCTION_TARGET_TYPE_TX &&
		(action == sanctiontypes.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER ||
			action == sanctiontypes.SanctionAction_SANCTION_ACTION_FREEZE_ADDRESS ||
			action == sanctiontypes.SanctionAction_SANCTION_ACTION_ESCROW_FUNDS) {
		return sanctiontypes.SanctionAction_SANCTION_ACTION_BLOCK_TX
	}
	return action
}
