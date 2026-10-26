package chainclient

import (
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
	"github.com/stretchr/testify/require"
)

func TestBroadcastRejectsInvalidAcknowledgement(t *testing.T) {
	for name, body := range map[string]string{
		"missing-result": `{"jsonrpc":"2.0","id":1}`,
		"null-result":    `{"jsonrpc":"2.0","id":1,"result":null}`,
		"empty-result":   `{"jsonrpc":"2.0","id":1,"result":{}}`,
		"missing-code":   `{"jsonrpc":"2.0","id":1,"result":{"hash":"ABC"}}`,
		"null-code":      `{"jsonrpc":"2.0","id":1,"result":{"code":null,"hash":"ABC"}}`,
		"missing-hash":   `{"jsonrpc":"2.0","id":1,"result":{"code":0}}`,
		"invalid-hash":   `{"jsonrpc":"2.0","id":1,"result":{"code":0,"hash":"not-hex"}}`,
		"wrong-hash":     `{"jsonrpc":"2.0","id":1,"result":{"code":0,"hash":"0000000000000000000000000000000000000000000000000000000000000000"}}`,
		"wrong-id":       `{"jsonrpc":"2.0","id":2,"result":{"code":0,"hash":"ABC"}}`,
		"wrong-version":  `{"jsonrpc":"1.0","id":1,"result":{"code":0,"hash":"ABC"}}`,
		"rpc-error":      `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"failure"}}`,
		"checktx-error":  `{"jsonrpc":"2.0","id":1,"result":{"code":32,"log":"sequence mismatch"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			body = strings.ReplaceAll(body, "ABC", hex.EncodeToString(tmhash.Sum([]byte("tx"))))
			client := New("http://comet.test", time.Second)
			client.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}
			_, err := client.BroadcastTxSync(context.Background(), []byte("tx"))
			require.Error(t, err)
		})
	}
}

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
				"hash": strings.ToUpper(hex.EncodeToString(tmhash.Sum([]byte("tx")))),
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
	if resp.Hash != strings.ToUpper(hex.EncodeToString(tmhash.Sum([]byte("tx")))) {
		t.Fatalf("unexpected hash: %s", resp.Hash)
	}
	if received != base64.StdEncoding.EncodeToString([]byte("tx")) {
		t.Fatalf("unexpected tx param: %s", received)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
