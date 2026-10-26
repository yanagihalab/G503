package signer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	secp "github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/yanagihalab/G503/x/sanction/keeper"
	"github.com/yanagihalab/G503/x/sanction/types"
	"google.golang.org/protobuf/proto"
)

type Signer struct {
	AgentID string
	PrivKey *secp256k1.PrivKey
	Address string
}

func New(agentID string, privateKeyHex string) (Signer, error) {
	priv, err := ParsePrivateKey(privateKeyHex)
	if err != nil {
		return Signer{}, err
	}
	configureAddressPrefix()
	return Signer{
		AgentID: agentID,
		PrivKey: priv,
		Address: sdk.AccAddress(priv.PubKey().Address()).String(),
	}, nil
}

// ParsePrivateKey rejects scalars outside [1, N-1] before SDK key derivation.
func ParsePrivateKey(privateKeyHex string) (*secp256k1.PrivKey, error) {
	keyBytes, err := hex.DecodeString(strings.TrimSpace(privateKeyHex))
	if err != nil {
		return nil, fmt.Errorf("invalid private key encoding")
	}
	if len(keyBytes) != secp256k1.PrivKeySize {
		return nil, fmt.Errorf("secp256k1 private key must be %d bytes", secp256k1.PrivKeySize)
	}
	var scalar secp.ModNScalar
	overflow := scalar.SetByteSlice(keyBytes)
	defer scalar.Zero()
	if overflow || scalar.IsZero() {
		return nil, fmt.Errorf("invalid secp256k1 private key scalar")
	}
	return &secp256k1.PrivKey{Key: keyBytes}, nil
}

func configureAddressPrefix() {
	defer func() {
		_ = recover()
	}()
	cfg := sdk.GetConfig()
	cfg.SetBech32PrefixForAccount("g503", "g503pub")
	cfg.SetBech32PrefixForValidator("g503valoper", "g503valoperpub")
	cfg.SetBech32PrefixForConsensusNode("g503valcons", "g503valconspub")
	cfg.Seal()
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

func (s Signer) SignVote(chainID string, vote *types.SanctionVote) (*types.SanctionVote, error) {
	if vote == nil {
		return nil, fmt.Errorf("vote is required")
	}
	vote = proto.Clone(vote).(*types.SanctionVote)
	vote.AgentId = s.AgentID
	signature, err := s.PrivKey.Sign(keeper.VoteSignBytes(chainID, vote))
	if err != nil {
		return vote, err
	}
	vote.Signature = signature
	return vote, nil
}

func (s Signer) SignRiskReport(chainID string, report *types.RiskReport) (*types.RiskReport, error) {
	if report == nil {
		return nil, fmt.Errorf("risk report is required")
	}
	report = proto.Clone(report).(*types.RiskReport)
	report.Submitter = s.Address
	signature, err := s.PrivKey.Sign(keeper.RiskReportSignBytes(chainID, report))
	if err != nil {
		return report, err
	}
	report.SubmitterSignature = signature
	return report, nil
}
