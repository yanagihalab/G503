package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cometbft/cometbft/crypto/tmhash"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/app"
	"github.com/yanagihalab/G503/dev/agent/internal/agentapi"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	"github.com/yanagihalab/G503/dev/agent/internal/riskclient"
	"github.com/yanagihalab/G503/dev/agent/internal/watcher"
	sanctiontypes "github.com/yanagihalab/G503/x/sanction/types"
)

type transportFunc func(*http.Request) (*http.Response, error)

func TestPendingBatchScreensAllTransfersAndSelectsBlockingRisk(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	screened := []string{}
	http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
		var value any
		if r.URL.Host == "risk.test" {
			var input riskclient.TxScreenRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
			screened = append(screened, input.Recipient)
			risk := riskclient.RiskResponse{Source: "chainalysis-mock", ResponseID: "risk", RiskScore: 99, RiskCategories: []string{"unknown"}}
			if input.Recipient == "risky" {
				risk.RiskScore = 86
				risk.RiskCategories = []string{"scam"}
			}
			value = risk
		} else {
			value = map[string]string{"rationale": "review"}
		}
		body, err := json.Marshal(value)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, err
	})
	result, err := processPendingTransfers(context.Background(), []watcher.DecodedTransfer{{TxHash: strings.Repeat("ab", 32), Recipient: "unknown"}, {TxHash: strings.Repeat("ab", 32), Recipient: "risky"}}, processOptions{riskURL: "http://risk.test", riskProvider: "mock", llmURL: "http://llm.test", llmProvider: "mock", targetType: "tx"})
	require.NoError(t, err)
	require.Equal(t, []string{"unknown", "risky"}, screened)
	require.Equal(t, "block_tx", result.Action)
}

func TestWatchBroadcastAdvancesSequenceAndRefreshesEvidenceHeight(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	transfers := []string{}
	for _, amount := range []int64{1, 2} {
		encoded, err := chainclient.EncodeUnsignedTx(&banktypes.MsgSend{FromAddress: sdk.AccAddress(bytes.Repeat([]byte{0x11}, 20)).String(), ToAddress: sdk.AccAddress(bytes.Repeat([]byte{0x22}, 20)).String(), Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", amount))})
		require.NoError(t, err)
		transfers = append(transfers, base64.StdEncoding.EncodeToString(encoded))
	}
	sequences := []uint64{}
	http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
		var value any
		switch {
		case r.URL.Path == "/unconfirmed_txs":
			value = map[string]any{"result": map[string]any{"txs": transfers}}
		case r.URL.Host == "risk.test":
			value = riskclient.RiskResponse{Source: "chainalysis-mock", ResponseID: "risk", RiskScore: 86, RiskCategories: []string{"scam"}}
		case r.URL.Host == "llm.test":
			value = map[string]string{"rationale": "review"}
		default:
			var rpc struct {
				ID     uint64 `json:"id"`
				Method string `json:"method"`
				Params struct {
					Tx string `json:"tx"`
				} `json:"params"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&rpc))
			if rpc.Method == "status" {
				value = map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{"sync_info": map[string]string{"latest_block_height": "150"}}}
			} else {
				require.Equal(t, "broadcast_tx_sync", rpc.Method)
				encoded, err := base64.StdEncoding.DecodeString(rpc.Params.Tx)
				require.NoError(t, err)
				tx, err := app.MakeEncodingConfig().TxConfig.TxDecoder()(encoded)
				require.NoError(t, err)
				sigs, err := tx.(interface {
					GetSignaturesV2() ([]signing.SignatureV2, error)
				}).GetSignaturesV2()
				require.NoError(t, err)
				sequences = append(sequences, sigs[0].Sequence)
				report := tx.GetMsgs()[0].(*sanctiontypes.MsgSubmitRiskReport).Report
				require.Equal(t, uint64(150), report.ObservedHeight)
				require.Equal(t, uint64(250), report.ExpiryHeight)
				value = map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{"code": 0, "hash": hex.EncodeToString(tmhash.Sum(encoded))}}
				if len(sequences) == 1 {
					value = map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{"code": 0, "hash": strings.Repeat("00", 32)}}
				}
				if len(sequences) == 3 {
					cancel()
				}
			}
		}
		body, err := json.Marshal(value)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, err
	})
	err := runWatch(ctx, processOptions{rpcURL: "http://rpc.test", riskURL: "http://risk.test", riskProvider: "mock", llmURL: "http://llm.test", llmProvider: "mock", agentID: "validator-1", privKeyHex: strings.Repeat("01", 32), broadcast: true, chainID: "watch-test", height: 1, expiryHeight: 101, sequence: 7, fee: "0stake", targetType: "tx"}, time.Millisecond, 10, io.Discard)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []uint64{7, 7, 8}, sequences)
}

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAgentDecisionBuildsSignedMessagesAndFailsClosed(t *testing.T) {
	for _, scenario := range []string{"approve", "policy-mismatch", "partial-evidence", "wrong-agent", "inconsistent-vote"} {
		t.Run(scenario, func(t *testing.T) {
			broadcasts := 0
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
				var value any
				switch r.URL.Host {
				case "risk.test":
					value = riskclient.RiskResponse{Source: "chainalysis-mock", ResponseID: "risk-1", RiskScore: 86, RiskCategories: []string{"scam"}, ExposureType: "direct"}
				case "agent.test":
					require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
					decision := agentapi.EvaluateResponse{AgentID: "validator-1", DecisionID: "decision-test", Approved: true, VoteOption: "approve", Action: "block_tx", CalibratedScore: 100, EvidenceStatus: "complete", Rationale: "mechanical review"}
					switch scenario {
					case "policy-mismatch":
						decision.Action = "revert_transfer"
					case "partial-evidence":
						decision.EvidenceStatus = "partial"
					case "wrong-agent":
						decision.AgentID = "validator-other"
					case "inconsistent-vote":
						decision.VoteOption = "reject"
					}
					value = decision
				case "llm.test":
					value = map[string]string{"rationale": "local explanation"}
				case "rpc.test":
					broadcasts++
					var request struct {
						ID     uint64 `json:"id"`
						Params struct {
							Tx string `json:"tx"`
						} `json:"params"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					txBytes, err := base64.StdEncoding.DecodeString(request.Params.Tx)
					require.NoError(t, err)
					tx, err := app.MakeEncodingConfig().TxConfig.TxDecoder()(txBytes)
					require.NoError(t, err)
					msgs := tx.GetMsgs()
					require.Len(t, msgs, 3)
					require.IsType(t, &sanctiontypes.MsgSubmitRiskReport{}, msgs[0])
					require.IsType(t, &sanctiontypes.MsgOpenSanctionCase{}, msgs[1])
					require.IsType(t, &sanctiontypes.MsgSubmitSanctionVote{}, msgs[2])
					value = map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"code": 0, "hash": hex.EncodeToString(tmhash.Sum(txBytes))}}
				default:
					t.Fatalf("unexpected HTTP destination: %s", r.URL)
				}
				body, err := json.Marshal(value)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, err
			})
			opts := processOptions{riskURL: "http://risk.test", riskProvider: "mock", llmURL: "http://llm.test", llmProvider: "mock", agentAPIURL: "http://agent.test", agentAPIToken: "test-token", agentID: "validator-1", targetType: "tx", privKeyHex: strings.Repeat("01", 32), chainID: "test-chain", height: 10, expiryHeight: 100, broadcast: true, rpcURL: "http://rpc.test", fee: "0stake"}
			result, err := processTx(context.Background(), inputTx{TxHash: strings.Repeat("ab", 32), Sender: sdk.AccAddress(bytes.Repeat([]byte{0x11}, 20)).String(), Recipient: sdk.AccAddress(bytes.Repeat([]byte{0x22}, 20)).String(), Amount: "10", Denom: "stake"}, opts)
			if scenario != "approve" {
				require.Error(t, err)
				require.Zero(t, broadcasts)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 1, broadcasts)
			require.Len(t, result.RiskReport.EvidenceHash, 32)
			require.Equal(t, result.RiskReport.LlmRationaleHash, result.Vote.RationaleHash)
			require.NotEmpty(t, result.RiskReport.SubmitterSignature)
			require.NotEmpty(t, result.Vote.Signature)
		})
	}
}
