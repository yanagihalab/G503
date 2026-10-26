package riskclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestScreenTx(t *testing.T) {
	client := New("http://risk.test", time.Second)
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/tx/screen" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		body, _ := json.Marshal(RiskResponse{
			Source:         "chainalysis-mock",
			ResponseID:     "risk-1",
			RiskScore:      86,
			RiskCategories: []string{"scam"},
			ExposureType:   "direct",
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Header:     make(http.Header),
		}, nil
	})}

	resp, err := client.ScreenTx(context.Background(), TxScreenRequest{TxHash: "abc", Recipient: "scam"})
	if err != nil {
		t.Fatalf("screen tx: %v", err)
	}
	if resp.RiskScore != 86 || resp.RiskCategories[0] != "scam" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestScreenTxChainalysisProvider(t *testing.T) {
	client := NewWithProvider("http://chainalysis.test", "chainalysis", "secret-token", time.Second)
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/address/g503recipient/risk" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("unexpected authorization header: %s", got)
		}
		body, _ := json.Marshal(RiskResponse{
			ResponseID:     "chainalysis-1",
			RiskScore:      91,
			RiskCategories: []string{"money_laundering"},
			ExposureType:   "direct",
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Header:     make(http.Header),
		}, nil
	})}

	resp, err := client.ScreenTx(context.Background(), TxScreenRequest{TxHash: "abc", Recipient: "g503recipient"})
	if err != nil {
		t.Fatalf("screen tx: %v", err)
	}
	if resp.Source != "chainalysis" || resp.TxHash != "abc" || resp.Address != "g503recipient" {
		t.Fatalf("unexpected normalized response: %+v", resp)
	}
}

func TestScreenTxChainalysisRequiresAPIKey(t *testing.T) {
	client := NewWithProvider("http://chainalysis.test", "chainalysis", "", time.Second)
	_, err := client.ScreenTx(context.Background(), TxScreenRequest{TxHash: "abc", Recipient: "g503recipient"})
	if err == nil {
		t.Fatalf("expected missing API key error")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
