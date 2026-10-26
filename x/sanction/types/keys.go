package types

const (
	ModuleName = "sanction"
	StoreKey   = ModuleName

	RouterKey = ModuleName
)

var (
	ParamsKey    = []byte{0x01}
	AuthorityKey = []byte{0x02}

	AgentKeyPrefix            = []byte{0x10}
	AgentBySignerKeyPrefix    = []byte{0x11}
	AgentLeaseKeyPrefix       = []byte{0x12}
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
	OriginalTransferKeyPrefix = []byte{0x70}
	SettlementKeyPrefix       = []byte{0x71}
	ReviewKeyPrefix           = []byte{0x72}
)
