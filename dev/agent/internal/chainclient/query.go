package chainclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cometbft/cometbft/crypto/tmhash"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	gogoproto "github.com/cosmos/gogoproto/proto"
	"github.com/yanagihalab/G503/app"
)

type Status struct {
	ChainID string
	Height  uint64
}

type Account struct {
	Number   uint64
	Sequence uint64
}

type TxResult struct {
	Found     bool
	Height    uint64
	Code      uint32
	Codespace string
	Log       string
	Tx        []byte
}

type QueryError struct {
	Code uint32
	Log  string
}

func (e *QueryError) Error() string { return fmt.Sprintf("ABCI query code=%d: %s", e.Code, e.Log) }

func (c *Client) Status(ctx context.Context) (Status, error) {
	raw, err := c.rpc(ctx, "status", map[string]any{})
	if err != nil {
		return Status{}, err
	}
	var wire struct {
		Node struct {
			Network string `json:"network"`
		} `json:"node_info"`
		Sync struct {
			Height string `json:"latest_block_height"`
		} `json:"sync_info"`
	}
	if err = json.Unmarshal(raw, &wire); err != nil {
		return Status{}, err
	}
	h, err := strconv.ParseUint(wire.Sync.Height, 10, 64)
	if err != nil || wire.Node.Network == "" {
		return Status{}, fmt.Errorf("invalid node status")
	}
	return Status{ChainID: wire.Node.Network, Height: h}, nil
}

// Query pins reads to a committed height, keeping account and semantic checks consistent.
func (c *Client) Query(ctx context.Context, path string, data []byte, height uint64) ([]byte, error) {
	raw, err := c.rpc(ctx, "abci_query", map[string]any{"path": path, "data": hex.EncodeToString(data), "height": strconv.FormatUint(height, 10), "prove": false})
	if err != nil {
		return nil, err
	}
	var wire struct {
		Response *struct {
			Code   uint32 `json:"code"`
			Log    string `json:"log"`
			Value  []byte `json:"value"`
			Height string `json:"height"`
		} `json:"response"`
	}
	if err = json.Unmarshal(raw, &wire); err != nil {
		return nil, err
	}
	if wire.Response == nil {
		return nil, fmt.Errorf("query missing response")
	}
	if wire.Response.Code != 0 {
		return nil, &QueryError{wire.Response.Code, wire.Response.Log}
	}
	if height != 0 && wire.Response.Height != strconv.FormatUint(height, 10) {
		return nil, fmt.Errorf("query height mismatch")
	}
	return wire.Response.Value, nil
}

func (c *Client) AccountAt(ctx context.Context, address string, height uint64) (Account, error) {
	bz, err := gogoproto.Marshal(&authtypes.QueryAccountRequest{Address: address})
	if err != nil {
		return Account{}, err
	}
	bz, err = c.Query(ctx, "/cosmos.auth.v1beta1.Query/Account", bz, height)
	if err != nil {
		return Account{}, err
	}
	var response authtypes.QueryAccountResponse
	if err = gogoproto.Unmarshal(bz, &response); err != nil {
		return Account{}, err
	}
	var account sdk.AccountI
	if err = app.MakeEncodingConfig().Codec.UnpackAny(response.Account, &account); err != nil {
		return Account{}, err
	}
	if account == nil {
		return Account{}, fmt.Errorf("account not found")
	}
	return Account{Number: account.GetAccountNumber(), Sequence: account.GetSequence()}, nil
}

func (c *Client) LookupTx(ctx context.Context, hash string) (TxResult, error) {
	bz, err := hex.DecodeString(hash)
	if err != nil || len(bz) != 32 {
		return TxResult{}, fmt.Errorf("invalid tx hash")
	}
	raw, err := c.rpc(ctx, "tx", map[string]any{"hash": base64.StdEncoding.EncodeToString(bz), "prove": false})
	if err != nil {
		var rpc *RPCError
		if errors.As(err, &rpc) && rpc.Code == -32603 && strings.EqualFold(rpc.Data, fmt.Sprintf("tx (%s) not found", hash)) {
			return TxResult{}, nil
		}
		return TxResult{}, err
	}
	var wire struct {
		Hash   string `json:"hash"`
		Height string `json:"height"`
		Result *struct {
			Code      *uint32 `json:"code"`
			Log       string  `json:"log"`
			Codespace string  `json:"codespace"`
		} `json:"tx_result"`
		Tx []byte `json:"tx"`
	}
	if err = json.Unmarshal(raw, &wire); err != nil {
		return TxResult{}, err
	}
	h, err := strconv.ParseUint(wire.Height, 10, 64)
	if err != nil || h == 0 || wire.Result == nil || wire.Result.Code == nil || !strings.EqualFold(hash, wire.Hash) {
		return TxResult{}, fmt.Errorf("invalid tx result")
	}
	if !strings.EqualFold(hash, hex.EncodeToString(tmhash.Sum(wire.Tx))) {
		return TxResult{}, fmt.Errorf("indexed transaction bytes do not match hash")
	}
	return TxResult{Found: true, Height: h, Code: *wire.Result.Code, Codespace: wire.Result.Codespace, Log: wire.Result.Log, Tx: wire.Tx}, nil
}

// Bind journals to a genesis, not merely a reusable chain-id.
func (c *Client) GenesisHash(ctx context.Context, chainID string) (string, error) {
	raw, err := c.rpc(ctx, "genesis", map[string]any{})
	if err != nil {
		return "", err
	}
	var wire struct {
		Genesis json.RawMessage `json:"genesis"`
	}
	if err = json.Unmarshal(raw, &wire); err != nil {
		return "", err
	}
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(wire.Genesis))
	decoder.UseNumber()
	if err = decoder.Decode(&value); err != nil {
		return "", err
	}
	if value["chain_id"] != chainID || value["app_state"] == nil || value["genesis_time"] == nil {
		return "", fmt.Errorf("invalid or mismatched genesis")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(canonical)
	return hex.EncodeToString(hash[:]), nil
}

func (c *Client) BlockTxs(ctx context.Context, height uint64) ([][]byte, []uint32, error) {
	params := map[string]string{"height": strconv.FormatUint(height, 10)}
	raw, err := c.rpc(ctx, "block", params)
	if err != nil {
		return nil, nil, err
	}
	var block struct {
		Block struct {
			Header struct {
				Height string `json:"height"`
			} `json:"header"`
			Data struct {
				Txs [][]byte `json:"txs"`
			} `json:"data"`
		} `json:"block"`
	}
	if err = json.Unmarshal(raw, &block); err != nil {
		return nil, nil, err
	}
	if block.Block.Header.Height != params["height"] {
		return nil, nil, fmt.Errorf("block height mismatch")
	}
	raw, err = c.rpc(ctx, "block_results", params)
	if err != nil {
		return nil, nil, err
	}
	var results struct {
		Height string `json:"height"`
		Txs    []struct {
			Code uint32 `json:"code"`
		} `json:"txs_results"`
	}
	if err = json.Unmarshal(raw, &results); err != nil {
		return nil, nil, err
	}
	if results.Height != params["height"] || len(results.Txs) != len(block.Block.Data.Txs) {
		return nil, nil, fmt.Errorf("block results mismatch")
	}
	codes := make([]uint32, len(results.Txs))
	for i, r := range results.Txs {
		codes[i] = r.Code
	}
	return block.Block.Data.Txs, codes, nil
}
