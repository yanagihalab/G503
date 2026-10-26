package watcher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

func TestPollingRecoversAfterRPCAndHandlerFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client := New("http://comet.test", time.Second)
	polls, attempts := 0, 0
	client.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		polls++
		if polls == 1 {
			return nil, fmt.Errorf("temporary RPC disconnect")
		}
		body := `{"result":{"txs":["dHgx"]}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	err := client.PollUnconfirmed(ctx, time.Millisecond, 10, func(context.Context, PendingTx) error {
		attempts++
		if attempts == 1 {
			return fmt.Errorf("temporary risk service failure")
		}
		cancel()
		return nil
	})
	if err != context.Canceled || attempts != 2 || polls < 3 {
		t.Fatalf("watch did not recover: polls=%d attempts=%d err=%v", polls, attempts, err)
	}
}

func TestUnconfirmedTxsRejectsRPCErrorEnvelope(t *testing.T) {
	client := New("http://comet.test", time.Second)
	client.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"error":{"code":-32603,"message":"unavailable"}}`)), Header: http.Header{}}, nil
	})
	if _, err := client.UnconfirmedTxs(context.Background(), 10); err == nil {
		t.Fatal("RPC error was mistaken for an empty mempool")
	}
}

func TestPollingForgetsTransactionsAfterLeavingMempool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client := New("http://comet.test", time.Second)
	polls, handled := 0, 0
	client.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		polls++
		body := `{"result":{"txs":["dHgx"]}}`
		if polls == 2 {
			body = `{"result":{"txs":[]}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	err := client.PollUnconfirmed(ctx, time.Millisecond, 10, func(context.Context, PendingTx) error {
		handled++
		if handled == 2 {
			cancel()
		}
		return nil
	})
	if err != context.Canceled || handled != 2 {
		t.Fatalf("stale dedup entry: handled=%d err=%v", handled, err)
	}
}
