package keeper

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"

	"github.com/yanagihalab/G503/x/sanction/types"
)

func VoteSignBytes(chainID string, vote types.SanctionVote) []byte {
	var out bytes.Buffer
	writeString(&out, chainID)
	writeString(&out, types.ModuleName)
	writeString(&out, vote.CaseId)
	writeString(&out, vote.AgentId)
	writeUint64(&out, uint64(vote.Option))
	writeUint64(&out, uint64(vote.ApprovedAction))
	writeString(&out, vote.ReasonCode)
	writeBytes(&out, vote.RationaleHash)
	writeUint64(&out, vote.SignedHeight)
	return out.Bytes()
}

func EvidenceHashStrings(parts ...string) []byte {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return h.Sum(nil)
}

func RiskReportSignBytes(chainID string, report types.RiskReport) []byte {
	copyReport := report
	copyReport.SubmitterSignature = nil
	var out bytes.Buffer
	writeString(&out, chainID)
	writeString(&out, types.ModuleName)
	writeBytes(&out, types.ModuleCdc.MustMarshal(&copyReport))
	return out.Bytes()
}

func VerifySecp256k1(publicKey []byte, signature []byte, payload []byte) bool {
	if len(publicKey) == 0 || len(signature) == 0 {
		return false
	}
	pubKey := secp256k1.PubKey{Key: publicKey}
	return pubKey.VerifySignature(payload, signature)
}

func writeString(out *bytes.Buffer, value string) {
	writeBytes(out, []byte(value))
}

func writeBytes(out *bytes.Buffer, value []byte) {
	writeUint64(out, uint64(len(value)))
	out.Write(value)
}

func writeUint64(out *bytes.Buffer, value uint64) {
	var bz [8]byte
	binary.BigEndian.PutUint64(bz[:], value)
	out.Write(bz[:])
}
