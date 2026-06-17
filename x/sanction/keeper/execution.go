package keeper

import (
	"fmt"

	"cosmossdk.io/errors"
	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/yanagihalab/G503/x/sanction/types"
)

func (k Keeper) IsAddressFrozen(ctx sdk.Context, address string) bool {
	_, found := k.GetFreezeRecord(ctx, address)
	return found
}

func (k Keeper) SendCoinsAllowed(ctx sdk.Context, from sdk.AccAddress, to sdk.AccAddress) error {
	if k.IsAddressFrozen(ctx, from.String()) {
		return errors.Wrapf(types.ErrAddressAlreadyFrozen, "sender=%s", from.String())
	}
	if k.IsAddressFrozen(ctx, to.String()) {
		return errors.Wrapf(types.ErrAddressAlreadyFrozen, "recipient=%s", to.String())
	}
	return nil
}

func (k Keeper) executeEscrow(ctx sdk.Context, sanctionCase types.SanctionCase) ([]byte, error) {
	if k.bankKeeper == nil {
		return nil, errors.Wrap(types.ErrExecutionUnsupported, "bank keeper is not configured")
	}
	report, err := k.primaryReport(ctx, sanctionCase)
	if err != nil {
		return nil, err
	}
	recipient, err := sdk.AccAddressFromBech32(report.Recipient)
	if err != nil {
		return nil, err
	}
	coins, err := reportCoins(report)
	if err != nil {
		return nil, err
	}
	if err := k.bankKeeper.SendCoinsFromAccountToModule(ctx, recipient, types.ModuleName, coins); err != nil {
		return nil, err
	}
	return stateChangeHash("escrow", sanctionCase.CaseId, report.Recipient, coins.String()), nil
}

func (k Keeper) executeRevert(ctx sdk.Context, sanctionCase types.SanctionCase) ([]byte, error) {
	if k.bankKeeper == nil {
		return nil, errors.Wrap(types.ErrExecutionUnsupported, "bank keeper is not configured")
	}
	report, err := k.primaryReport(ctx, sanctionCase)
	if err != nil {
		return nil, err
	}
	sender, err := sdk.AccAddressFromBech32(report.Sender)
	if err != nil {
		return nil, err
	}
	recipient, err := sdk.AccAddressFromBech32(report.Recipient)
	if err != nil {
		return nil, err
	}
	coins, err := reportCoins(report)
	if err != nil {
		return nil, err
	}
	if err := k.bankKeeper.SendCoins(ctx, recipient, sender, coins); err != nil {
		return nil, err
	}
	return stateChangeHash("revert", sanctionCase.CaseId, report.Recipient, report.Sender, coins.String()), nil
}

func (k Keeper) primaryReport(ctx sdk.Context, sanctionCase types.SanctionCase) (types.RiskReport, error) {
	if len(sanctionCase.ReportIds) == 0 {
		return types.RiskReport{}, errors.Wrap(types.ErrRiskReportNotFound, "case has no reports")
	}
	report, found := k.GetRiskReport(ctx, sanctionCase.ReportIds[0])
	if !found {
		return types.RiskReport{}, errors.Wrapf(types.ErrRiskReportNotFound, "report=%s", sanctionCase.ReportIds[0])
	}
	return report, nil
}

func reportCoins(report types.RiskReport) (sdk.Coins, error) {
	if report.Amount == "" || report.Denom == "" {
		return nil, fmt.Errorf("report amount and denom are required")
	}
	amount, ok := sdkmath.NewIntFromString(report.Amount)
	if !ok {
		return nil, fmt.Errorf("invalid amount: %s", report.Amount)
	}
	coin := sdk.NewCoin(report.Denom, amount)
	return sdk.NewCoins(coin), nil
}

func stateChangeHash(parts ...string) []byte {
	return EvidenceHashStrings(parts...)
}
