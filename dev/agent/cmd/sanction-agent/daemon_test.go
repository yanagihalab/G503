package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	"github.com/yanagihalab/G503/dev/agent/internal/signer"
	"github.com/yanagihalab/G503/dev/agent/internal/txmanager"
	"github.com/yanagihalab/G503/dev/agent/internal/watcher"
	"github.com/yanagihalab/G503/x/sanction/keeper"
	st "github.com/yanagihalab/G503/x/sanction/types"
	"google.golang.org/protobuf/proto"
)

func TestDaemonRejectsCaseMetadataNotMatchingOriginalTransfer(t *testing.T) {
	sender := sdk.AccAddress(bytes.Repeat([]byte{1}, 20)).String()
	recipient := sdk.AccAddress(bytes.Repeat([]byte{2}, 20)).String()
	bz, err := chainclient.EncodeUnsignedTx(&banktypes.MsgSend{FromAddress: sender, ToAddress: recipient, Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", 10))})
	require.NoError(t, err)
	hash := hex.EncodeToString(keeper.TxHash(bz))
	for _, scenario := range []string{"match", "wrong-amount", "wrong-recipient", "wrong-index", "failed-original"} {
		t.Run(scenario, func(t *testing.T) {
			r := &st.RiskReport{ReportId: "report", TxHash: keeper.TxHash(bz), Sender: sender, Recipient: recipient, Amount: "10", Denom: "stake"}
			if scenario == "wrong-amount" {
				r.Amount = "11"
			}
			if scenario == "wrong-recipient" {
				r.Recipient = sender
			}
			if scenario == "wrong-index" {
				r.MessageIndex = 1
			}
			c := chainclient.New("http://node.test", time.Second)
			c.HTTPClient = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				var rpc struct {
					ID     uint64 `json:"id"`
					Method string `json:"method"`
				}
				require.NoError(t, json.NewDecoder(req.Body).Decode(&rpc))
				var result any
				if rpc.Method == "tx" {
					code := 0
					if scenario == "failed-original" {
						code = 5
					}
					result = map[string]any{"hash": hash, "height": "2", "tx": bz, "tx_result": map[string]any{"code": code}}
				} else {
					value, e := proto.Marshal(&st.QueryRiskReportResponse{Report: r})
					require.NoError(t, e)
					result = map[string]any{"response": map[string]any{"code": 0, "height": "10", "value": value}}
				}
				body, e := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, e
			})}
			d := &daemon{rpc: c}
			w := &daemonWork{Hash: hash}
			err := d.bindOriginal(context.Background(), w, &st.SanctionCase{TxHash: keeper.TxHash(bz), TargetAddress: recipient, ReportIds: []string{"report"}}, 10)
			if scenario == "match" {
				require.NoError(t, err)
				require.Len(t, w.Transfers, 1)
			} else {
				require.Error(t, err)
				require.Empty(t, w.Transfers)
			}
		})
	}
}

func TestHistoricalCaseMemberDoesNotDependOnCurrentActiveFlag(t *testing.T) {
	s, err := signer.New("old-member", strings.Repeat("01", 32))
	require.NoError(t, err)
	d := &daemon{opts: processOptions{agentID: "old-member"}, signer: s}
	c := &st.SanctionCase{VotingSnapshotVersion: 1, Voters: []*st.AgentInfo{{AgentId: "old-member", SignerAddress: s.Address, PublicKey: s.PublicKey(), Active: true, VotingPower: 7}}}
	require.True(t, d.caseMember(c))
	c.Voters[0].PublicKey = bytes.Repeat([]byte{2}, 33)
	require.False(t, d.caseMember(c))
	c.Voters = nil
	require.False(t, d.caseMember(c))
}

func TestDurableObservationSurvivesReplayBeforeCheckpoint(t *testing.T) {
	bz, err := chainclient.EncodeUnsignedTx(&banktypes.MsgSend{FromAddress: sdk.AccAddress(bytes.Repeat([]byte{1}, 20)).String(), ToAddress: sdk.AccAddress(bytes.Repeat([]byte{2}, 20)).String(), Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", 10))})
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "agent.db")
	s, err := txmanager.Open(path, "test")
	require.NoError(t, err)
	d := &daemon{store: s, encoder: json.NewEncoder(io.Discard)}
	tx := watcher.PendingTx{Bytes: bz, Hash: hex.EncodeToString(keeper.TxHash(bz))}
	require.NoError(t, d.observe(tx, 10))
	require.NoError(t, s.Close())
	s, err = txmanager.Open(path, "test")
	require.NoError(t, err)
	defer s.Close()
	d.store = s
	require.NoError(t, d.observe(tx, 12))
	rows, err := s.List("work")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	var work daemonWork
	require.NoError(t, json.Unmarshal(rows[0], &work))
	require.Equal(t, uint64(10), work.SeenHeight)
}

func TestReconciliationDoesNotBroadcastObsoleteEvaluations(t *testing.T) {
	id := "auto-" + strings.Repeat("AB", 32)
	for _, scenario := range []string{"fresh", "changed-action", "expired-review", "superseded", "no-action", "executed"} {
		t.Run(scenario, func(t *testing.T) {
			s, err := txmanager.Open(filepath.Join(t.TempDir(), "agent.db"), "test")
			require.NoError(t, err)
			defer s.Close()
			w := daemonWork{Hash: strings.Repeat("AB", 32), CaseID: id, Review: &output{Action: "revert_transfer", RiskReport: &st.RiskReport{ExpiryHeight: 20}}}
			c := &st.SanctionCase{CaseId: id, Status: st.CaseStatus_CASE_STATUS_APPROVED, RequestedAction: st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER}
			kind := "execute"
			switch scenario {
			case "changed-action":
				w.Review.Action = "none"
			case "expired-review":
				w.Review.RiskReport.ExpiryHeight = 9
			case "superseded":
				w.CaseID = id + "-r1"
				w.Review = nil
			case "no-action":
				c = nil
				kind = "open"
				w.Done = true
				w.Result = "no_action"
			case "executed":
				c.ExecutedHeight = 9
				c.Status = st.CaseStatus_CASE_STATUS_EXECUTED
				w.Review = nil
			}
			require.NoError(t, s.Put("work", id, w))
			rpc := chainclient.New("http://node.test", time.Second)
			rpc.HTTPClient = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				var req struct {
					ID     uint64 `json:"id"`
					Params struct {
						Path string `json:"path"`
					} `json:"params"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
				var message proto.Message
				if strings.HasSuffix(req.Params.Path, "/ExecutionRecord") {
					message = &st.QueryExecutionRecordResponse{Record: &st.ExecutionRecord{CaseId: id, ResultCode: "ok"}}
				} else {
					response := &st.QuerySanctionCasesByTxResponse{}
					if c != nil {
						response.SanctionCases = []*st.SanctionCase{c}
					}
					message = response
				}
				value, e := proto.Marshal(message)
				require.NoError(t, e)
				body, e := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"response": map[string]any{"code": 0, "height": "10", "value": value}}})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, e
			})}
			d := &daemon{store: s, rpc: rpc}
			done, err := d.satisfied(context.Background(), txmanager.Intent{Kind: kind, CaseID: id}, 10)
			switch scenario {
			case "fresh":
				require.NoError(t, err)
				require.False(t, done)
			case "executed":
				require.NoError(t, err)
				require.True(t, done)
			case "expired-review":
				require.ErrorIs(t, err, txmanager.ErrReevaluationRequired)
			default:
				require.ErrorIs(t, err, txmanager.ErrObsolete)
			}
		})
	}
}

func TestExecutionReconciliationRequiresActionSpecificResult(t *testing.T) {
	hash := strings.Repeat("AB", 32)
	id := autoCaseID(hash)
	for _, scenario := range []struct {
		name     string
		action   st.SanctionAction
		record   *st.ExecutionRecord
		complete bool
	}{
		{"watch-success", st.SanctionAction_SANCTION_ACTION_WATCH, &st.ExecutionRecord{CaseId: id, ResultCode: "watch_only"}, true},
		{"watch-wrong-result", st.SanctionAction_SANCTION_ACTION_WATCH, &st.ExecutionRecord{CaseId: id, ResultCode: "ok"}, false},
		{"revert-success", st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER, &st.ExecutionRecord{CaseId: id, ResultCode: "ok"}, true},
		{"revert-watch-result", st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER, &st.ExecutionRecord{CaseId: id, ResultCode: "watch_only"}, false},
		{"watch-missing-record", st.SanctionAction_SANCTION_ACTION_WATCH, nil, false},
		{"watch-wrong-case", st.SanctionAction_SANCTION_ACTION_WATCH, &st.ExecutionRecord{CaseId: "other", ResultCode: "watch_only"}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			c := &st.SanctionCase{CaseId: id, RequestedAction: scenario.action, Status: st.CaseStatus_CASE_STATUS_EXECUTED, ExecutedHeight: 9}
			rpc := chainclient.New("http://node.test", time.Second)
			rpc.HTTPClient = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				var request struct {
					ID     uint64 `json:"id"`
					Params struct {
						Path string `json:"path"`
					} `json:"params"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				var response proto.Message
				switch request.Params.Path {
				case "/sanction.v1.Query/SanctionCasesByTx":
					response = &st.QuerySanctionCasesByTxResponse{SanctionCases: []*st.SanctionCase{c}}
				case "/sanction.v1.Query/ExecutionRecord":
					response = &st.QueryExecutionRecordResponse{Record: scenario.record}
				default:
					t.Fatalf("unexpected query: %s", request.Params.Path)
				}
				value, err := proto.Marshal(response)
				require.NoError(t, err)
				body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"response": map[string]any{"code": 0, "height": "10", "value": value}}})
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, err
			})}
			d := &daemon{rpc: rpc, encoder: json.NewEncoder(io.Discard)}
			done, err := d.satisfied(context.Background(), txmanager.Intent{Kind: "execute", CaseID: id}, 10)
			require.NoError(t, err)
			require.Equal(t, scenario.complete, done)
			work := &daemonWork{Hash: hash, CaseID: id}
			err = d.progress(context.Background(), work, 10, 0, nil, &st.Params{})
			if scenario.complete {
				require.NoError(t, err)
				require.True(t, work.Done)
				require.Equal(t, "executed", work.Result)
			} else {
				require.Error(t, err)
				require.False(t, work.Done)
			}
		})
	}
}
