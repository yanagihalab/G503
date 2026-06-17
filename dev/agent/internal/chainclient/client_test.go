package chainclient

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestBroadcastTxSync(t *testing.T) {
	var received string
	client := New("http://comet.test", time.Second)
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var req struct {
			Method string            `json:"method"`
			Params map[string]string `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode rpc: %v", err)
		}
		received = req.Params["tx"]
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"code": 0,
				"hash": "ABC",
			},
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Header:     make(http.Header),
		}, nil
	})}

	resp, err := client.BroadcastTxSync(context.Background(), []byte("tx"))
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if resp.Hash != "ABC" {
		t.Fatalf("unexpected hash: %s", resp.Hash)
	}
	if received != "0x"+hex.EncodeToString([]byte("tx")) {
		t.Fatalf("unexpected tx param: %s", received)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
