package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/dev/agent/internal/txmanager"
	st "github.com/yanagihalab/G503/x/sanction/types"
)

type recoverySnapshot struct {
	Work []struct {
		CaseID string `json:"case_id"`
		Review *struct {
			Report *st.RiskReport `json:"risk_report"`
		} `json:"review"`
	} `json:"work"`
	Intents []txmanager.Intent `json:"intents"`
}

func recoveryRead(t *testing.T, home, name string, out any) {
	t.Helper()
	bz, err := os.ReadFile(filepath.Join(home, "logs", name))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(bz, out))
}

func TestAgentRecovery(t *testing.T) {
	l := lab(t)
	var one, two struct {
		Hash string `json:"tx_hash"`
	}
	recoveryRead(t, l.home, "recovery-transfer1.json", &one)
	recoveryRead(t, l.home, "recovery-transfer2.json", &two)
	case1, case2 := "auto-"+one.Hash, "auto-"+two.Hash
	retries := 0
	for i := 1; i <= 5; i++ {
		var old, fixed, restored recoverySnapshot
		recoveryRead(t, l.home, fmt.Sprintf("recovery-agent%d-failed.json", i), &old)
		recoveryRead(t, l.home, fmt.Sprintf("recovery-agent%d-retried.json", i), &fixed)
		recoveryRead(t, l.home, fmt.Sprintf("recovery-agent%d-restored.json", i), &restored)
		require.Len(t, old.Intents, 1)
		require.Equal(t, "failed", old.Intents[0].Status)
		require.Equal(t, uint32(11), old.Intents[0].Attempts[0].CheckCode)
		var oldReport, newReport *st.RiskReport
		for _, w := range old.Work {
			if w.CaseID == case1 && w.Review != nil {
				oldReport = w.Review.Report
			}
		}
		for _, w := range fixed.Work {
			if w.CaseID == case1 && w.Review != nil {
				newReport = w.Review.Report
			}
		}
		require.NotNil(t, oldReport)
		require.NotNil(t, newReport)
		require.Greater(t, newReport.ObservedHeight, oldReport.ExpiryHeight)
		require.NotEqual(t, oldReport.EvidenceHash, newReport.EvidenceHash)
		for _, intent := range fixed.Intents {
			require.True(t, intent.Status == "confirmed" || intent.Status == "satisfied" || intent.Status == "cancelled")
			if intent.ID == "open/"+case1 {
				retries += len(intent.Recoveries)
			}
		}
		require.Len(t, restored.Intents, 1)
		require.Equal(t, "satisfied", restored.Intents[0].Status)
		require.Len(t, restored.Intents[0].Attempts, 1, "stale failed backup must not sign again")
		var backup txmanager.BackupResult
		recoveryRead(t, l.home, fmt.Sprintf("backup-agent%d-lost-ack.json", i), &backup)
		require.NotEmpty(t, backup.SHA256)
		require.NotEmpty(t, backup.Manifest.GenesisHash)
		var result struct {
			Restored bool `json:"restored"`
		}
		recoveryRead(t, l.home, fmt.Sprintf("restore-agent%d-lost-ack.json", i), &result)
		require.True(t, result.Restored)
	}
	require.Positive(t, retries)
	before, err := os.ReadFile(filepath.Join(l.home, "logs/recovery-sequences-before.txt"))
	require.NoError(t, err)
	after, err := os.ReadFile(filepath.Join(l.home, "logs/recovery-sequences-after.txt"))
	require.NoError(t, err)
	start, err := os.ReadFile(filepath.Join(l.home, "logs/recovery-restore-start-height.txt"))
	require.NoError(t, err)
	end, err := os.ReadFile(filepath.Join(l.home, "logs/recovery-restore-end-height.txt"))
	require.NoError(t, err)
	from, err := strconv.Atoi(strings.TrimSpace(string(start)))
	require.NoError(t, err)
	to, err := strconv.Atoi(strings.TrimSpace(string(end)))
	require.NoError(t, err)
	leaseCounts := map[string]uint64{}
	for h := from + 1; h <= to; h++ {
		resp, err := http.Get(fmt.Sprintf("%s/block_results?height=%d", l.rpcURL, h))
		require.NoError(t, err)
		var b struct {
			Result struct {
				Txs []struct {
					Code   uint32 `json:"code"`
					Events []struct {
						Type       string                        `json:"type"`
						Attributes []struct{ Key, Value string } `json:"attributes"`
					} `json:"events"`
				} `json:"txs_results"`
			} `json:"result"`
		}
		err = json.NewDecoder(resp.Body).Decode(&b)
		resp.Body.Close()
		require.NoError(t, err)
		for _, tx := range b.Result.Txs {
			if tx.Code != 0 {
				continue
			}
			for _, e := range tx.Events {
				if e.Type == "agent_lease_claimed" {
					for _, a := range e.Attributes {
						if a.Key == "signer" {
							leaseCounts[a.Value]++
						}
					}
				}
			}
		}
	}
	oldSequences, newSequences := strings.Fields(string(before)), strings.Fields(string(after))
	require.Len(t, oldSequences, 5)
	require.Len(t, newSequences, 5)
	for i := range oldSequences {
		a, err := strconv.ParseUint(oldSequences[i], 10, 64)
		require.NoError(t, err)
		b, err := strconv.ParseUint(newSequences[i], 10, 64)
		require.NoError(t, err)
		require.Equal(t, a+leaseCounts[l.keys[i]["agent"].Address], b, "only lease acquisition/renewal may consume sequence after restoring an already-satisfied intent")
	}
	oldCase, newCase := &st.QuerySanctionCaseResponse{}, &st.QuerySanctionCaseResponse{}
	require.NoError(t, l.query(l.rpc, "SanctionCase", &st.QuerySanctionCaseRequest{CaseId: case2}, oldCase))
	require.Equal(t, st.CaseStatus_CASE_STATUS_EXPIRED, oldCase.SanctionCase.Status)
	require.NoError(t, l.query(l.rpc, "SanctionCase", &st.QuerySanctionCaseRequest{CaseId: case2 + "-r1"}, newCase))
	require.Equal(t, st.CaseStatus_CASE_STATUS_EXECUTED, newCase.SanctionCase.Status)
	require.Equal(t, case2, newCase.SanctionCase.PreviousCaseId)
	agents := &st.QueryAgentsResponse{}
	require.NoError(t, l.query(l.rpc, "Agents", &st.QueryAgentsRequest{}, agents))
	for _, id := range []string{case1, case2 + "-r1"} {
		votes := &st.QuerySanctionVotesResponse{}
		require.NoError(t, l.query(l.rpc, "SanctionVotes", &st.QuerySanctionVotesRequest{CaseId: id}, votes))
		require.Len(t, votes.Votes, 5)
		seen := map[string]bool{}
		for _, v := range votes.Votes {
			require.Equal(t, st.VoteOption_VOTE_OPTION_APPROVE, v.Option)
			require.False(t, seen[v.AgentId])
			seen[v.AgentId] = true
		}
	}
	for _, hash := range []string{one.Hash, two.Hash} {
		s := &st.QuerySettlementsResponse{}
		require.NoError(t, l.query(l.rpc, "Settlements", &st.QuerySettlementsRequest{TxHash: hash}, s))
		require.Len(t, s.Settlements, 1)
	}
	status, err := l.rpc.Status(l.ctx)
	require.NoError(t, err)
	executions := 0
	for h := uint64(1); h <= status.Height; h++ {
		resp, err := http.Get(fmt.Sprintf("%s/block_results?height=%d", l.rpcURL, h))
		require.NoError(t, err)
		var block struct {
			Result struct {
				Txs []struct {
					Code   uint32 `json:"code"`
					Events []struct {
						Type       string                        `json:"type"`
						Attributes []struct{ Key, Value string } `json:"attributes"`
					} `json:"events"`
				} `json:"txs_results"`
			} `json:"result"`
		}
		err = json.NewDecoder(resp.Body).Decode(&block)
		resp.Body.Close()
		require.NoError(t, err)
		for _, tx := range block.Result.Txs {
			if tx.Code != 0 {
				continue
			}
			for _, event := range tx.Events {
				if event.Type != "sanction_executed" {
					continue
				}
				for _, a := range event.Attributes {
					if a.Key == "case_id" && (a.Value == case1 || a.Value == case2 || a.Value == case2+"-r1") {
						executions++
					}
				}
			}
		}
	}
	require.Equal(t, 2, executions)
	var balances struct{ Sender, Recipient string }
	recoveryRead(t, l.home, "recovery-balances.json", &balances)
	require.Equal(t, balances.Sender, l.balance(l.keys[0]["faucet"].Address))
	require.Equal(t, balances.Recipient, l.balance(l.keys[0]["demo"].Address))
	l.save("recovery-metrics.json", map[string]any{"validators": 5, "agents": 5, "expired_evidence_re_evaluated": true, "explicit_failed_retry": true, "execute_ack_backup_restored": true, "stale_failed_backup_no_rebroadcast": true, "expired_case_fresh_five_votes": true, "successful_recovery_executions": executions, "duplicate_executions": 0, "balances_restored": true})
}
