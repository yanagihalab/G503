package watcher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	Endpoint   string
	HTTPClient *http.Client
}

type PendingTx struct {
	Bytes []byte
	Hash  string
}

type Handler func(context.Context, PendingTx) error

func New(endpoint string, timeout time.Duration) Client {
	return Client{
		Endpoint:   strings.TrimRight(endpoint, "/"),
		HTTPClient: &http.Client{Timeout: timeout},
	}
}

func (c Client) UnconfirmedTxs(ctx context.Context, limit int) ([]PendingTx, error) {
	if limit <= 0 {
		limit = 100
	}
	url := fmt.Sprintf("%s/unconfirmed_txs?limit=%d", c.Endpoint, limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("comet rpc returned %s", resp.Status)
	}
	var body struct {
		Result struct {
			Txs []string `json:"txs"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]PendingTx, 0, len(body.Result.Txs))
	for _, encoded := range body.Result.Txs {
		tx, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, err
		}
		out = append(out, PendingTx{Bytes: tx})
	}
	return out, nil
}

func (c Client) PollUnconfirmed(ctx context.Context, interval time.Duration, limit int, handler Handler) error {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	seen := map[string]struct{}{}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		txs, err := c.UnconfirmedTxs(ctx, limit)
		if err != nil {
			return err
		}
		for _, tx := range txs {
			key := string(tx.Bytes)
			if _, found := seen[key]; found {
				continue
			}
			seen[key] = struct{}{}
			if err := handler(ctx, tx); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
