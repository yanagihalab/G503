package types

import (
	"bytes"
	"fmt"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

func ValidateCaseSnapshot(c *SanctionCase) error {
	if c.VotingSnapshotVersion != 1 || len(c.Voters) == 0 || c.QuorumThreshold == 0 || c.QuorumThreshold > 100 {
		return fmt.Errorf("case %s requires an explicit valid voter snapshot", c.CaseId)
	}
	seenSigners := map[string]bool{}
	previous := ""
	for _, a := range c.Voters {
		if a == nil || ValidateAgent(a) != nil || !a.Active || a.VotingPower == 0 || len(a.PublicKey) == 0 || a.AgentId <= previous || seenSigners[a.SignerAddress] {
			return fmt.Errorf("invalid or duplicate case voter")
		}
		previous = a.AgentId
		seenSigners[a.SignerAddress] = true
		address, err := sdk.AccAddressFromBech32(a.SignerAddress)
		if err != nil || len(a.PublicKey) != 33 || !bytes.Equal(address, (&secp256k1.PubKey{Key: a.PublicKey}).Address()) {
			return fmt.Errorf("case voter key does not match signer")
		}
	}
	return nil
}
