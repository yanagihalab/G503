package llmclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestExplainMock(t *testing.T) {
	client := New("http://llm.test", "mock", "", time.Second)
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/explain" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		body, _ := json.Marshal(ExplainResponse{
			RecommendedAction: "block",
			Rationale:         "risky",
			Caveats:           "mock",
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Header:     make(http.Header),
		}, nil
	})}

	resp, err := client.Explain(context.Background(), ExplainRequest{Task: "risk_explanation"})
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if resp.RecommendedAction != "block" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
