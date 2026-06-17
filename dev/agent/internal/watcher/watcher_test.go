package watcher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUnconfirmedTxs(t *testing.T) {
	client := New("http://comet.test", time.Second)
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := json.Marshal(map[string]any{
			"result": map[string]any{
				"txs": []string{base64.StdEncoding.EncodeToString([]byte("tx1"))},
			},
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Header:     make(http.Header),
		}, nil
	})}

	txs, err := client.UnconfirmedTxs(context.Background(), 10)
	if err != nil {
		t.Fatalf("unconfirmed txs: %v", err)
	}
	if len(txs) != 1 || string(txs[0].Bytes) != "tx1" {
		t.Fatalf("unexpected txs: %+v", txs)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
