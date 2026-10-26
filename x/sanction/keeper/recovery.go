package keeper

import (
	"bytes"

	"cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/yanagihalab/G503/x/sanction/types"
)

// Retain every old report and vote. A replacement is a new, independently voted case.
func (k Keeper) expireForReplacement(ctx sdk.Context, previous string, next *types.RiskReport, target types.SanctionTargetType) error {
	c, found := k.GetSanctionCase(ctx, previous)
	if !found {
		return types.ErrCaseNotFound
	}
	if target != types.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS || c.TargetType != target || c.TargetAddress != next.Recipient || !bytes.Equal(c.TxHash, next.TxHash) {
		return errors.Wrap(types.ErrInvalidCase, "replacement must target the same address transfer")
	}
	if c.ExecutedHeight != 0 || (c.Status != types.CaseStatus_CASE_STATUS_PENDING && c.Status != types.CaseStatus_CASE_STATUS_APPROVED && c.Status != types.CaseStatus_CASE_STATUS_EXPIRED) {
		return errors.Wrap(types.ErrCaseNotPending, "executed/rejected/revoked cases cannot be renewed")
	}
	if _, found := k.GetExecutionRecord(ctx, previous); found {
		return types.ErrAlreadySettled
	}
	r, err := k.primaryReport(ctx, c)
	if err != nil {
		return err
	}
	height := uint64(ctx.BlockHeight())
	if r.Sender != next.Sender || r.Recipient != next.Recipient || r.Amount != next.Amount || r.Denom != next.Denom || r.MessageIndex != next.MessageIndex || next.ReportId == r.ReportId || next.ObservedHeight <= r.ObservedHeight || next.ObservedHeight > height {
		return errors.Wrap(types.ErrInvalidCase, "replacement requires newly observed evidence for the same transfer")
	}
	if !(r.ExpiryHeight <= height || (c.Status == types.CaseStatus_CASE_STATUS_PENDING && c.VotingDeadlineHeight < height) || c.Status == types.CaseStatus_CASE_STATUS_EXPIRED) {
		return errors.Wrap(types.ErrInvalidCase, "previous evidence and voting period are still valid")
	}
	for _, other := range k.GetSanctionCasesByTx(ctx, next.TxHash) {
		if other.PreviousCaseId == previous {
			return errors.Wrap(types.ErrDuplicateCase, "previous case already has a replacement")
		}
	}
	c.Status = types.CaseStatus_CASE_STATUS_EXPIRED
	k.SetSanctionCase(ctx, c)
	return nil
}
