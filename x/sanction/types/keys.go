package types

const (
	ModuleName = "sanction"
	StoreKey   = ModuleName

	RouterKey = ModuleName
)

var (
	ParamsKey = []byte{0x01}

	AgentKeyPrefix            = []byte{0x10}
	AgentBySignerKeyPrefix    = []byte{0x11}
	RiskReportKeyPrefix       = []byte{0x20}
	RiskReportByTxKeyPrefix   = []byte{0x21}
	RiskReportByAddrKeyPrefix = []byte{0x22}
	SanctionCaseKeyPrefix     = []byte{0x30}
	CaseByTxKeyPrefix         = []byte{0x31}
	CaseByAddrKeyPrefix       = []byte{0x32}
	SanctionVoteKeyPrefix     = []byte{0x40}
	ActiveTxSanctionKeyPrefix = []byte{0x50}
	FrozenAddressKeyPrefix    = []byte{0x51}
	ExecutionRecordKeyPrefix  = []byte{0x60}
)
