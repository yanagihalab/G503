package chainclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cometbft/cometbft/crypto/tmhash"
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

func (e *RPCError) Error() string {
	return fmt.Sprintf("rpc error %d: %s: %s", e.Code, e.Message, e.Data)
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

func (c *Client) CurrentHeight(ctx context.Context) (uint64, error) {
	raw, err := c.rpc(ctx, "status", map[string]any{})
	if err != nil {
		return 0, err
	}
	var status struct {
		SyncInfo struct {
			LatestBlockHeight string `json:"latest_block_height"`
		} `json:"sync_info"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return 0, err
	}
	height, err := strconv.ParseUint(status.SyncInfo.LatestBlockHeight, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid chain height: %w", err)
	}
	return height, nil
}

func (c *Client) BroadcastTxSync(ctx context.Context, tx []byte) (BroadcastResult, error) {
	var result BroadcastResult
	raw, err := c.rpc(ctx, "broadcast_tx_sync", map[string]string{
		"tx": base64.StdEncoding.EncodeToString(tx),
	})
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	var acknowledgement struct {
		Code *uint32 `json:"code"`
	}
	if err := json.Unmarshal(raw, &acknowledgement); err != nil {
		return result, err
	}
	if acknowledgement.Code == nil {
		return result, fmt.Errorf("broadcast response is missing a result code")
	}
	if result.Code != 0 {
		return result, fmt.Errorf("broadcast failed code=%d log=%s", result.Code, result.Log)
	}
	hash, err := hex.DecodeString(result.Hash)
	if err != nil || !bytes.Equal(hash, tmhash.Sum(tx)) {
		return result, fmt.Errorf("broadcast response hash does not match submitted tx")
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
	if rpcResp.JSONRPC != "2.0" || rpcResp.ID != id {
		return nil, fmt.Errorf("rpc response version or id does not match request")
	}
	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}
	if len(rpcResp.Result) == 0 || bytes.Equal(bytes.TrimSpace(rpcResp.Result), []byte("null")) {
		return nil, fmt.Errorf("rpc response is missing a result")
	}
	return rpcResp.Result, nil
}
