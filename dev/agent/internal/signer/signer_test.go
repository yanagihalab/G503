package signer

import (
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/x/sanction/keeper"
	"github.com/yanagihalab/G503/x/sanction/types"
	"google.golang.org/protobuf/proto"
)

func TestNewRejectsInvalidPrivateScalars(t *testing.T) {
	for name, key := range map[string]string{
		"zero":        strings.Repeat("00", 32),
		"order":       "fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141",
		"above-order": "fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364142",
		"maximum":     strings.Repeat("ff", 32),
		"short":       "01",
		"invalid-hex": strings.Repeat("gg", 32),
	} {
		t.Run(name, func(t *testing.T) {
			require.NotPanics(t, func() {
				_, err := New("agent-1", key)
				require.Error(t, err)
			})
		})
	}
}

func TestPrivateScalarBoundariesSignAndVerify(t *testing.T) {
	for _, key := range []string{strings.Repeat("00", 31) + "01", "fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364140"} {
		parsed, err := ParsePrivateKey(" \n" + key + "\n")
		require.NoError(t, err)
		require.Equal(t, key, hex.EncodeToString(parsed.Key))
		message := []byte("scalar boundary test")
		sig, err := parsed.Sign(message)
		require.NoError(t, err)
		require.True(t, parsed.PubKey().VerifySignature(message, sig))
	}
}

func TestPointerSigningClonesInputsAndNestedFields(t *testing.T) {
	s, err := New("signer-agent", strings.Repeat("01", 32))
	require.NoError(t, err)
	vote := &types.SanctionVote{CaseId: "case", AgentId: "original", Signature: []byte{9}, RationaleHash: []byte{3, 4}, Option: types.VoteOption_VOTE_OPTION_APPROVE}
	report := &types.RiskReport{ReportId: "report", Submitter: "original", SubmitterSignature: []byte{8}, EvidenceHash: []byte{1, 2}, RiskCategories: []string{"scam"}}
	voteBefore, err := proto.Marshal(vote)
	require.NoError(t, err)
	reportBefore, err := proto.Marshal(report)
	require.NoError(t, err)
	signedVote, err := s.SignVote("test", vote)
	require.NoError(t, err)
	signedReport, err := s.SignRiskReport("test", report)
	require.NoError(t, err)
	require.NotSame(t, vote, signedVote)
	require.NotSame(t, report, signedReport)
	require.Equal(t, s.AgentID, signedVote.AgentId)
	require.Equal(t, s.Address, signedReport.Submitter)
	require.True(t, keeper.VerifySecp256k1(s.PublicKey(), signedVote.Signature, keeper.VoteSignBytes("test", signedVote)))
	require.True(t, keeper.VerifySecp256k1(s.PublicKey(), signedReport.SubmitterSignature, keeper.RiskReportSignBytes("test", signedReport)))
	signedVote.RationaleHash[0]++
	signedReport.EvidenceHash[0]++
	signedReport.RiskCategories[0] = "changed"
	voteAfter, err := proto.Marshal(vote)
	require.NoError(t, err)
	reportAfter, err := proto.Marshal(report)
	require.NoError(t, err)
	require.Equal(t, voteBefore, voteAfter)
	require.Equal(t, reportBefore, reportAfter)
	_, err = s.SignVote("test", nil)
	require.Error(t, err)
	_, err = s.SignRiskReport("test", nil)
	require.Error(t, err)
}

func TestConcurrentSigningAndHashingDoNotMutateSharedMessages(t *testing.T) {
	s, err := New("agent", strings.Repeat("01", 32))
	require.NoError(t, err)
	report := &types.RiskReport{ReportId: "report", EvidenceHash: []byte{1}, SubmitterSignature: []byte{2}}
	vote := &types.SanctionVote{CaseId: "case", RationaleHash: []byte{3}}
	caseInfo := &types.SanctionCase{CaseId: "case", DecisionHash: []byte{4}, Voters: []*types.AgentInfo{{AgentId: "agent", PublicKey: []byte{5}}}}
	before := []proto.Message{proto.Clone(report), proto.Clone(vote), proto.Clone(caseInfo)}
	var wg sync.WaitGroup
	errors := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 4 {
				v, err := s.SignVote("test", vote)
				if err != nil {
					errors <- err
					return
				}
				r, err := s.SignRiskReport("test", report)
				if err != nil {
					errors <- err
					return
				}
				if !keeper.VerifySecp256k1(s.PublicKey(), v.Signature, keeper.VoteSignBytes("test", v)) || !keeper.VerifySecp256k1(s.PublicKey(), r.SubmitterSignature, keeper.RiskReportSignBytes("test", r)) {
					errors <- fmt.Errorf("signature verification failed")
					return
				}
				_ = keeper.DecisionHash(caseInfo)
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	for n, after := range []proto.Message{report, vote, caseInfo} {
		require.True(t, proto.Equal(before[n], after), "shared input %d was mutated", n)
	}
}
