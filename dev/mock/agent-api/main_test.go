package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEvaluateMechanicalPolicy(t *testing.T) {
	tests := []struct {
		name           string
		req            evaluateRequest
		wantAction     string
		wantMinScore   uint32
		wantMaxScore   uint32
		wantReview     string
		wantEvidence   string
		wantConfidence bool
	}{
		{
			name: "low risk rejects sanction",
			req: evaluateRequest{
				AgentID:    "validator-1",
				TargetType: "tx",
				Tx:         txInput{TxHash: "aa", Sender: "g5031sender", Recipient: "g5031recipient", Amount: "100", Denom: "stake"},
				Risk:       riskInput{Source: "mock", ResponseID: "risk-aa", Score: 12, Categories: []string{"low_risk"}, ExposureType: "none"},
			},
			wantAction:     "none",
			wantMaxScore:   20,
			wantReview:     "auto_reject",
			wantEvidence:   "complete",
			wantConfidence: true,
		},
		{
			name: "unconfirmed high risk tx is blocked",
			req: evaluateRequest{
				AgentID:    "validator-2",
				TargetType: "tx",
				Tx:         txInput{TxHash: "bb", Sender: "g5031sender", Recipient: "g5031recipient", Amount: "100", Denom: "stake"},
				Risk:       riskInput{Source: "mock", ResponseID: "risk-bb", Score: 86, Categories: []string{"scam"}, ExposureType: "direct"},
			},
			wantAction:     "block_tx",
			wantMinScore:   90,
			wantReview:     "critical",
			wantEvidence:   "complete",
			wantConfidence: true,
		},
		{
			name: "confirmed tx target still follows chain block policy",
			req: evaluateRequest{
				AgentID:    "validator-3",
				TargetType: "tx",
				Tx:         txInput{TxHash: "cc", Sender: "g5031sender", Recipient: "g5031recipient", Amount: "1000000", Denom: "stake", Confirmed: true},
				Risk:       riskInput{Source: "mock", ResponseID: "risk-cc", Score: 95, Categories: []string{"money_laundering"}, ExposureType: "direct"},
			},
			wantAction:     "block_tx",
			wantMinScore:   90,
			wantReview:     "critical",
			wantEvidence:   "complete",
			wantConfidence: true,
		},
		{
			name: "trusted address reduces action",
			req: evaluateRequest{
				AgentID:    "validator-4",
				TargetType: "tx",
				Tx:         txInput{TxHash: "dd", Sender: "g5031trusted", Recipient: "g5031recipient", Amount: "100", Denom: "stake"},
				Risk:       riskInput{Source: "mock", ResponseID: "risk-dd", Score: 72, Categories: []string{"scam"}, ExposureType: "direct"},
				Policy:     policyInput{TrustedAddresses: []string{"g5031trusted"}},
			},
			wantAction:     "watch",
			wantMinScore:   60,
			wantMaxScore:   75,
			wantReview:     "monitor",
			wantEvidence:   "complete",
			wantConfidence: true,
		},
		{
			name: "insufficient evidence caps decision to watch",
			req: evaluateRequest{
				AgentID:    "validator-5",
				TargetType: "address",
				Tx:         txInput{TxHash: "ee", Sender: "g5031sender", Amount: "100", Denom: "stake"},
				Risk:       riskInput{Score: 99, Categories: []string{"sanctioned"}, ExposureType: "direct"},
			},
			wantAction:     "watch",
			wantMinScore:   60,
			wantReview:     "manual_review_required",
			wantEvidence:   "insufficient",
			wantConfidence: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applyDefaults(&tt.req)
			got := evaluate(tt.req)
			if got.Action != tt.wantAction {
				t.Fatalf("action=%s, want %s; rationale=%s", got.Action, tt.wantAction, got.Rationale)
			}
			if got.Score < tt.wantMinScore {
				t.Fatalf("score=%d, want >= %d", got.Score, tt.wantMinScore)
			}
			if tt.wantMaxScore > 0 && got.Score > tt.wantMaxScore {
				t.Fatalf("score=%d, want <= %d", got.Score, tt.wantMaxScore)
			}
			if got.ReviewLevel != tt.wantReview {
				t.Fatalf("review=%s, want %s", got.ReviewLevel, tt.wantReview)
			}
			if got.EvidenceStatus != tt.wantEvidence {
				t.Fatalf("evidence=%s, want %s", got.EvidenceStatus, tt.wantEvidence)
			}
			if tt.wantConfidence && (got.Confidence <= 0 || got.Confidence > 0.99) {
				t.Fatalf("confidence=%f out of expected range", got.Confidence)
			}
			if len(got.Findings) == 0 {
				t.Fatal("expected findings")
			}
		})
	}
}

func TestDecisionIDIsDeterministic(t *testing.T) {
	req := evaluateRequest{
		AgentID:    "validator-1",
		TargetType: "tx",
		Tx:         txInput{TxHash: "aa", Recipient: "g5031recipient"},
		Risk:       riskInput{ResponseID: "risk-aa", Categories: []string{"scam"}},
	}
	applyDefaults(&req)
	first := decisionID(req)
	second := decisionID(req)
	if first != second {
		t.Fatalf("decision id changed: %s != %s", first, second)
	}
	req.Risk.Categories = []string{"money_laundering"}
	applyDefaults(&req)
	if first == decisionID(req) {
		t.Fatal("decision id should change when material risk evidence changes")
	}
	baseline := decisionID(req)
	req.Risk.Score = 99
	if baseline == decisionID(req) {
		t.Fatal("decision id must bind the risk score")
	}
	baseline = decisionID(req)
	req.Policy.BlockedAddresses = []string{"g5031recipient"}
	if baseline == decisionID(req) {
		t.Fatal("decision id must bind the policy")
	}
}

func TestEvidenceAndCategoryCannotBeBypassed(t *testing.T) {
	req := evaluateRequest{AgentID: "validator-1", TargetType: "tx",
		Tx:   txInput{TxHash: "aa", Recipient: "recipient", Amount: "100"},
		Risk: riskInput{Score: 99, ExposureType: "direct"}}
	applyDefaults(&req)
	if got := evaluate(req); got.Action != "watch" || got.EvidenceStatus != "insufficient" {
		t.Fatalf("missing evidence bypassed safeguard: %+v", got)
	}
	req.Risk.Source, req.Risk.ResponseID = "mock", "risk-aa"
	req.Risk.Categories = []string{"unknown"}
	if got := evaluate(req); got.Action != "watch" {
		t.Fatalf("unrecognized category escalated to sanction: %+v", got)
	}
}

func TestAPIAuthBodyLimitAndRateLimit(t *testing.T) {
	handler := newAPIHandler("secret", parseSet("validator-agent"), 1, 256, 0)
	request := func(token, role, address, body string) int {
		r := httptest.NewRequest(http.MethodPost, "/v1/agents/evaluate", strings.NewReader(body))
		r.RemoteAddr = address
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Agent-Role", role)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if code := request("wrong", "validator-agent", "127.0.0.1:1001", "{}"); code != 401 {
		t.Fatalf("auth code=%d", code)
	}
	if code := request("secret", "admin", "127.0.0.1:1001", "{}"); code != 403 {
		t.Fatalf("role code=%d", code)
	}
	body := `{"agent_id":"a","tx":{"tx_hash":"aa"},"risk":{"score":10}}`
	if code := request("secret", "validator-agent", "127.0.0.1:1001", body); code != 200 {
		t.Fatalf("evaluation code=%d", code)
	}
	if code := request("secret", "validator-agent", "127.0.0.1:1002", body); code != 429 {
		t.Fatalf("port rotation bypassed limit: code=%d", code)
	}
	large := `{"agent_id":"` + strings.Repeat("a", 512) + `"}`
	if code := request("secret", "validator-agent", "127.0.0.2:1001", large); code != 413 {
		t.Fatalf("body limit code=%d", code)
	}
}
