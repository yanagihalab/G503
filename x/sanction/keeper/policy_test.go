package keeper

import (
	"testing"

	"github.com/yanagihalab/G503/x/sanction/types"
)

func TestComputePolicyActionDowncastsPreFinalityTxToBlock(t *testing.T) {
	params := types.DefaultParams()
	reports := []types.RiskReport{{
		RiskScore:      92,
		RiskCategories: []string{"money_laundering"},
	}}

	action := ComputePolicyAction(reports, params, types.SanctionTargetType_SANCTION_TARGET_TYPE_TX)
	if action != types.SanctionAction_SANCTION_ACTION_BLOCK_TX {
		t.Fatalf("expected block tx, got %s", types.ActionName(action))
	}
}

func TestComputePolicyActionRequiresHighRiskCategory(t *testing.T) {
	params := types.DefaultParams()
	reports := []types.RiskReport{{
		RiskScore:      95,
		RiskCategories: []string{"unknown"},
	}}

	action := ComputePolicyAction(reports, params, types.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS)
	if action != types.SanctionAction_SANCTION_ACTION_WATCH {
		t.Fatalf("expected watch, got %s", types.ActionName(action))
	}
}
