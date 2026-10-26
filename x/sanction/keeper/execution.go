package keeper

import (
	"bytes"
	"fmt"

	"cosmossdk.io/errors"
	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/yanagihalab/G503/x/sanction/types"
)

type settlementContextKey struct{}

type settlementTransfer struct {
	from string
	to   string
}

func (k Keeper) IsAddressFrozen(ctx sdk.Context, address string) bool {
	_, found := k.GetFreezeRecord(ctx, address)
	return found
}

func (k Keeper) SendCoinsAllowed(ctx sdk.Context, from sdk.AccAddress, to sdk.AccAddress) error {
	if transfer, ok := ctx.Value(settlementContextKey{}).(settlementTransfer); ok && transfer.from == from.String() && transfer.to == to.String() {
		return nil
	}
	if k.IsAddressFrozen(ctx, from.String()) {
		return errors.Wrapf(types.ErrAddressAlreadyFrozen, "sender=%s", from.String())
	}
	if k.IsAddressFrozen(ctx, to.String()) {
		return errors.Wrapf(types.ErrAddressAlreadyFrozen, "recipient=%s", to.String())
	}
	return nil
}

func (k Keeper) executeEscrow(ctx sdk.Context, sanctionCase *types.SanctionCase) ([]byte, error) {
	if k.bankKeeper == nil {
		return nil, errors.Wrap(types.ErrExecutionUnsupported, "bank keeper is not configured")
	}
	report, err := k.primaryReport(ctx, sanctionCase)
	if err != nil {
		return nil, err
	}
	original, err := k.unsettledOriginal(ctx, report)
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
	settlementCtx := settlementContext(ctx, recipient, authtypes.NewModuleAddress(types.ModuleName))
	if err := k.bankKeeper.SendCoinsFromAccountToModule(settlementCtx, recipient, types.ModuleName, coins); err != nil {
		return nil, err
	}
	k.SetSettlement(ctx, &types.SettlementRecord{Transfer: original, CaseId: sanctionCase.CaseId, Action: sanctionCase.RequestedAction, ExecutedHeight: uint64(ctx.BlockHeight())})
	return stateChangeHash("escrow", sanctionCase.CaseId, report.Recipient, coins.String()), nil
}

func (k Keeper) executeRevert(ctx sdk.Context, sanctionCase *types.SanctionCase) ([]byte, error) {
	if k.bankKeeper == nil {
		return nil, errors.Wrap(types.ErrExecutionUnsupported, "bank keeper is not configured")
	}
	report, err := k.primaryReport(ctx, sanctionCase)
	if err != nil {
		return nil, err
	}
	original, err := k.unsettledOriginal(ctx, report)
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
	if err := k.bankKeeper.SendCoins(settlementContext(ctx, recipient, sender), recipient, sender, coins); err != nil {
		return nil, err
	}
	k.SetSettlement(ctx, &types.SettlementRecord{Transfer: original, CaseId: sanctionCase.CaseId, Action: sanctionCase.RequestedAction, ExecutedHeight: uint64(ctx.BlockHeight())})
	return stateChangeHash("revert", sanctionCase.CaseId, report.Recipient, report.Sender, coins.String()), nil
}

// Only quorum-authorized settlement calls receive this private, endpoint-scoped exemption.
func settlementContext(ctx sdk.Context, from, to sdk.AccAddress) sdk.Context {
	return ctx.WithValue(settlementContextKey{}, settlementTransfer{from: from.String(), to: to.String()})
}

func (k Keeper) primaryReport(ctx sdk.Context, sanctionCase *types.SanctionCase) (*types.RiskReport, error) {
	if len(sanctionCase.ReportIds) == 0 {
		return &types.RiskReport{}, errors.Wrap(types.ErrRiskReportNotFound, "case has no reports")
	}
	report, found := k.GetRiskReport(ctx, sanctionCase.ReportIds[0])
	if !found {
		return &types.RiskReport{}, errors.Wrapf(types.ErrRiskReportNotFound, "report=%s", sanctionCase.ReportIds[0])
	}
	if !bytes.Equal(report.TxHash, sanctionCase.TxHash) || report.Recipient != sanctionCase.TargetAddress {
		return &types.RiskReport{}, errors.Wrap(types.ErrOriginalTransfer, "case and primary report differ")
	}
	return report, nil
}

func reportCoins(report *types.RiskReport) (sdk.Coins, error) {
	if report.Amount == "" || report.Denom == "" {
		return nil, fmt.Errorf("report amount and denom are required")
	}
	amount, ok := sdkmath.NewIntFromString(report.Amount)
	if !ok || !amount.IsPositive() {
		return nil, fmt.Errorf("invalid amount: %s", report.Amount)
	}
	if err := sdk.ValidateDenom(report.Denom); err != nil {
		return nil, err
	}
	coin := sdk.NewCoin(report.Denom, amount)
	return sdk.NewCoins(coin), nil
}

func stateChangeHash(parts ...string) []byte {
	return EvidenceHashStrings(parts...)
}
