package signer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/yanagihalab/G503/x/sanction/keeper"
	"github.com/yanagihalab/G503/x/sanction/types"
)

type Signer struct {
	AgentID string
	PrivKey *secp256k1.PrivKey
	Address string
}

func New(agentID string, privateKeyHex string) (Signer, error) {
	keyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return Signer{}, err
	}
	if len(keyBytes) != secp256k1.PrivKeySize {
		return Signer{}, fmt.Errorf("secp256k1 private key must be %d bytes", secp256k1.PrivKeySize)
	}
	priv := &secp256k1.PrivKey{Key: keyBytes}
	return Signer{
		AgentID: agentID,
		PrivKey: priv,
		Address: sdk.AccAddress(priv.PubKey().Address()).String(),
	}, nil
}

func (s Signer) PublicKey() []byte {
	return s.PrivKey.PubKey().Bytes()
}

func EvidenceHash(parts ...[]byte) []byte {
	h := sha256.New()
	for _, part := range parts {
		h.Write(part)
	}
	return h.Sum(nil)
}

func RationaleHash(rationale string, caveats string) []byte {
	sum := sha256.Sum256([]byte(rationale + "\n" + caveats))
	return sum[:]
}

func (s Signer) SignVote(chainID string, vote types.SanctionVote) (types.SanctionVote, error) {
	vote.AgentId = s.AgentID
	signature, err := s.PrivKey.Sign(keeper.VoteSignBytes(chainID, vote))
	if err != nil {
		return vote, err
	}
	vote.Signature = signature
	return vote, nil
}

func (s Signer) SignRiskReport(chainID string, report types.RiskReport) (types.RiskReport, error) {
	report.Submitter = s.Address
	signature, err := s.PrivKey.Sign(keeper.RiskReportSignBytes(chainID, report))
	if err != nil {
		return report, err
	}
	report.SubmitterSignature = signature
	return report, nil
}
