package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestEvaluateSendsAuthAndRoleHeaders(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer token-1" {
			t.Fatalf("missing authorization header: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Agent-Role") != "validator-agent" {
			t.Fatalf("missing role header: %q", r.Header.Get("X-Agent-Role"))
		}
		var req EvaluateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.AgentID != "validator-1" || req.Tx.TxHash != "abc" {
			t.Fatalf("unexpected request: %+v", req)
		}
		var body bytes.Buffer
		_ = json.NewEncoder(&body).Encode(EvaluateResponse{
			AgentID:         req.AgentID,
			DecisionID:      "decision-1",
			Approved:        true,
			VoteOption:      "approve",
			Action:          "block_tx",
			RawRiskScore:    86,
			CalibratedScore: 100,
			ReviewLevel:     "critical",
			EvidenceStatus:  "complete",
			Findings:        []string{"base_risk_score=86"},
			Deterministic:   true,
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(&body),
		}, nil
	})

	client, err := New("http://agent-api.test", "token-1", "validator-agent", time.Second, TLSConfig{})
	if err != nil {
		t.Fatal(err)
	}
	client.HTTPClient.Transport = transport
	resp, raw, err := client.Evaluate(context.Background(), EvaluateRequest{
		AgentID:    "validator-1",
		TargetType: "tx",
		Tx:         TxInput{TxHash: "abc"},
		Risk:       RiskInput{Score: 86},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Action != "block_tx" || resp.CalibratedScore != 100 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(raw) == 0 {
		t.Fatal("expected raw response bytes")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
