package types

import "cosmossdk.io/errors"

var (
	ErrAgentNotFound        = errors.Register(ModuleName, 2, "agent not found")
	ErrAgentInactive        = errors.Register(ModuleName, 3, "agent inactive")
	ErrDuplicateAgent       = errors.Register(ModuleName, 4, "duplicate agent")
	ErrRiskReportNotFound   = errors.Register(ModuleName, 5, "risk report not found")
	ErrRiskReportExpired    = errors.Register(ModuleName, 6, "risk report expired")
	ErrInvalidRiskScore     = errors.Register(ModuleName, 7, "invalid risk score")
	ErrSourceNotAccepted    = errors.Register(ModuleName, 8, "risk source not accepted")
	ErrCaseNotFound         = errors.Register(ModuleName, 9, "sanction case not found")
	ErrDuplicateCase        = errors.Register(ModuleName, 10, "duplicate sanction case")
	ErrCaseNotPending       = errors.Register(ModuleName, 11, "sanction case is not pending")
	ErrVoteAlreadySubmitted = errors.Register(ModuleName, 12, "vote already submitted")
	ErrInvalidSignature     = errors.Register(ModuleName, 13, "invalid signature")
	ErrQuorumNotReached     = errors.Register(ModuleName, 14, "quorum not reached")
	ErrActionNotAllowed     = errors.Register(ModuleName, 15, "sanction action not allowed")
	ErrTxAlreadySanctioned  = errors.Register(ModuleName, 16, "transaction already sanctioned")
	ErrAddressAlreadyFrozen = errors.Register(ModuleName, 17, "address already frozen")
	ErrExecutionUnsupported = errors.Register(ModuleName, 18, "execution unsupported")
	ErrUnauthorized         = errors.Register(ModuleName, 19, "unauthorized")
	ErrInvalidCase          = errors.Register(ModuleName, 20, "invalid sanction case")
)
