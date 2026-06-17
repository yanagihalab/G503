package chainclient

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

type Client struct {
	Endpoint   string
	HTTPClient *http.Client
	nextID     uint64
}

type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

type BroadcastResult struct {
	Code      uint32 `json:"code"`
	Data      string `json:"data"`
	Log       string `json:"log"`
	Hash      string `json:"hash"`
	Codespace string `json:"codespace"`
}

func New(endpoint string, timeout time.Duration) *Client {
	return &Client{
		Endpoint: strings.TrimRight(endpoint, "/"),
		HTTPClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *Client) BroadcastTxSync(ctx context.Context, tx []byte) (BroadcastResult, error) {
	var result BroadcastResult
	raw, err := c.rpc(ctx, "broadcast_tx_sync", map[string]string{
		"tx": "0x" + hex.EncodeToString(tx),
	})
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	if result.Code != 0 {
		return result, fmt.Errorf("broadcast failed code=%d log=%s", result.Code, result.Log)
	}
	return result, nil
}

func (c *Client) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := atomic.AddUint64(&c.nextID, 1)
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
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
		return nil, fmt.Errorf("rpc returned %s", resp.Status)
	}
	var rpcResp RPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, err
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}
