package keeper

import (
	"bytes"
	"encoding/hex"
)

import "github.com/yanagihalab/G503/x/sanction/types"

func agentKey(agentID string) []byte {
	return append(types.AgentKeyPrefix, []byte(agentID)...)
}

func agentBySignerKey(signer string) []byte {
	return append(types.AgentBySignerKeyPrefix, []byte(signer)...)
}

func riskReportKey(reportID string) []byte {
	return append(types.RiskReportKeyPrefix, []byte(reportID)...)
}

func riskReportByTxKey(txHash []byte, reportID string) []byte {
	key := append([]byte{}, types.RiskReportByTxKeyPrefix...)
	key = append(key, txHash...)
	key = append(key, 0x00)
	return append(key, []byte(reportID)...)
}

func riskReportByAddressKey(address string, reportID string) []byte {
	key := append([]byte{}, types.RiskReportByAddrKeyPrefix...)
	key = append(key, []byte(address)...)
	key = append(key, 0x00)
	return append(key, []byte(reportID)...)
}

func sanctionCaseKey(caseID string) []byte {
	return append(types.SanctionCaseKeyPrefix, []byte(caseID)...)
}

func caseByTxKey(txHash []byte, caseID string) []byte {
	key := append([]byte{}, types.CaseByTxKeyPrefix...)
	key = append(key, txHash...)
	key = append(key, 0x00)
	return append(key, []byte(caseID)...)
}

func caseByAddressKey(address string, caseID string) []byte {
	key := append([]byte{}, types.CaseByAddrKeyPrefix...)
	key = append(key, []byte(address)...)
	key = append(key, 0x00)
	return append(key, []byte(caseID)...)
}

func sanctionVoteKey(caseID string, agentID string) []byte {
	key := append([]byte{}, types.SanctionVoteKeyPrefix...)
	key = append(key, []byte(caseID)...)
	key = append(key, 0x00)
	return append(key, []byte(agentID)...)
}

func activeTxSanctionKey(txHash []byte) []byte {
	return append(types.ActiveTxSanctionKeyPrefix, txHash...)
}

func frozenAddressKey(address string) []byte {
	return append(types.FrozenAddressKeyPrefix, []byte(address)...)
}

func executionRecordKey(caseID string) []byte {
	return append(types.ExecutionRecordKeyPrefix, []byte(caseID)...)
}

func txPrefix(prefix []byte, txHash []byte) []byte {
	key := append([]byte{}, prefix...)
	return append(key, txHash...)
}

func splitIndexedValue(value []byte) (string, bool) {
	if len(value) == 0 {
		return "", false
	}
	return string(value), true
}

func parseHexHash(hexHash string) ([]byte, error) {
	return hex.DecodeString(hexHash)
}

func hashEqual(left []byte, right []byte) bool {
	return bytes.Equal(left, right)
}
