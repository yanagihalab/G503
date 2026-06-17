package types

func ActionName(action SanctionAction) string {
	switch action {
	case SanctionAction_SANCTION_ACTION_WATCH:
		return "watch"
	case SanctionAction_SANCTION_ACTION_BLOCK_TX:
		return "block_tx"
	case SanctionAction_SANCTION_ACTION_FREEZE_ADDRESS:
		return "freeze_address"
	case SanctionAction_SANCTION_ACTION_ESCROW_FUNDS:
		return "escrow_funds"
	case SanctionAction_SANCTION_ACTION_REVERT_TRANSFER:
		return "revert_transfer"
	default:
		return "unspecified"
	}
}
