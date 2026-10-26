package rpcguard

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	codec "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	auth "github.com/cosmos/cosmos-sdk/x/auth/types"
	bank "github.com/cosmos/cosmos-sdk/x/bank/types"
	gogo "github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	"github.com/yanagihalab/G503/dev/agent/internal/signer"
)

type fixture struct {
	mu           sync.Mutex
	age          time.Duration
	chain, hash  string
	lost, absent bool
	tx           []byte
	sends        int
	sequence     uint64
}

func server(t *testing.T) (*fixture, *httptest.Server) {
	f := &fixture{chain: "guard-test", hash: strings.Repeat("AB", 32)}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var req struct {
			ID     uint64
			Method string
			Params map[string]any
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var result any
		switch req.Method {
		case "status":
			result = map[string]any{"node_info": map[string]string{"network": f.chain}, "sync_info": map[string]any{"latest_block_height": "12", "latest_block_hash": f.hash, "latest_block_time": time.Now().Add(-f.age), "catching_up": false}}
		case "genesis":
			result = map[string]any{"genesis": map[string]any{"chain_id": f.chain, "genesis_time": "2026-10-26T00:00:00Z", "app_state": map[string]any{}}}
		case "block":
			result = map[string]any{"block_id": map[string]string{"hash": f.hash}, "block": map[string]any{"header": map[string]any{"chain_id": f.chain, "height": req.Params["height"], "time": time.Now().Add(-f.age)}}}
		case "broadcast_tx_sync":
			f.sends++
			f.tx, _ = base64.StdEncoding.DecodeString(fmt.Sprint(req.Params["tx"]))
			if f.lost {
				http.Error(w, "lost ack", 503)
				return
			}
			h := sha256.Sum256(f.tx)
			result = map[string]any{"code": 0, "hash": hex.EncodeToString(h[:])}
		case "tx":
			hash, _ := base64.StdEncoding.DecodeString(fmt.Sprint(req.Params["hash"]))
			if f.absent {
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32603, "message": "Internal error", "data": fmt.Sprintf("tx (%s) not found", strings.ToUpper(hex.EncodeToString(hash)))}})
				return
			}
			result = map[string]any{"hash": hex.EncodeToString(hash), "height": "12", "tx": f.tx, "tx_result": map[string]any{"code": 0}}
		case "abci_query":
			a, err := codec.NewAnyWithValue(&auth.BaseAccount{Sequence: f.sequence})
			require.NoError(t, err)
			bz, err := gogo.Marshal(&auth.QueryAccountResponse{Account: a})
			require.NoError(t, err)
			result = map[string]any{"response": map[string]any{"code": 0, "height": "12", "value": bz}}
		default:
			http.Error(w, "unexpected method", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(s.Close)
	return f, s
}

func TestGuardPersistsHistoryAndNeverReplacesAmbiguousSend(t *testing.T) {
	f, s := server(t)
	ctx := context.Background()
	options := Options{StateDir: filepath.Join(t.TempDir(), "rpc"), Policy: chainclient.RPCSafety{MaxHeightLag: 2}}
	key := strings.Repeat("01", 32)
	signer, err := signer.New("test", key)
	require.NoError(t, err)
	sign := chainclient.SignOptions{ChainID: "guard-test", PrivateKeyHex: key, GasLimit: 200000}
	msg := &bank.MsgSend{FromAddress: signer.Address, ToAddress: signer.Address, Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", 1))}
	g, err := Open("guard-test", s.URL, options)
	require.NoError(t, err)
	f.lost = true
	_, err = g.Send(ctx, "transfer-1", sign, msg)
	require.Error(t, err)
	require.NoError(t, g.Close())
	g, err = Open("guard-test", s.URL, options)
	require.NoError(t, err)
	defer g.Close()
	sign.Sequence = 99
	result, err := g.Send(ctx, "transfer-1", sign, msg)
	require.NoError(t, err)
	require.NotEmpty(t, result.Hash)
	f.mu.Lock()
	require.Equal(t, 1, f.sends)
	f.mu.Unlock()
	msg.Amount = sdk.NewCoins(sdk.NewInt64Coin("stake", 2))
	_, err = g.Send(ctx, "transfer-1", sign, msg)
	require.ErrorContains(t, err, "different operation")
	msg.Amount = sdk.NewCoins(sdk.NewInt64Coin("stake", 1))
	f.mu.Lock()
	f.absent = true
	f.sequence = 1
	f.mu.Unlock()
	_, err = g.Send(ctx, "transfer-1", sign, msg)
	require.ErrorContains(t, err, "sequence changed")
	f.mu.Lock()
	require.Equal(t, 1, f.sends)
	f.hash = strings.Repeat("CD", 32)
	f.mu.Unlock()
	_, err = g.Client.Status(ctx)
	require.ErrorContains(t, err, "continuity")
}

func TestGuardStopsStaleAndWrongChainSends(t *testing.T) {
	for _, scenario := range []string{"stale", "wrong-chain"} {
		t.Run(scenario, func(t *testing.T) {
			f, s := server(t)
			if scenario == "stale" {
				f.age = time.Minute
			} else {
				f.chain = "wrong"
			}
			g, err := Open("guard-test", s.URL, Options{StateDir: filepath.Join(t.TempDir(), "rpc")})
			require.NoError(t, err)
			defer g.Close()
			_, err = g.Client.BroadcastTxSync(context.Background(), []byte("not sent"))
			require.Error(t, err)
			f.mu.Lock()
			defer f.mu.Unlock()
			require.Zero(t, f.sends)
		})
	}
}

func TestConcurrentSameRequestSendsOnlyOnce(t *testing.T) {
	f, s := server(t)
	g, err := Open("guard-test", s.URL, Options{StateDir: filepath.Join(t.TempDir(), "rpc")})
	require.NoError(t, err)
	defer g.Close()
	key := strings.Repeat("01", 32)
	signer, err := signer.New("concurrent", key)
	require.NoError(t, err)
	msg := &bank.MsgSend{FromAddress: signer.Address, ToAddress: signer.Address, Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", 1))}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := g.Send(context.Background(), "same", chainclient.SignOptions{ChainID: "guard-test", PrivateKeyHex: key}, msg)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Equal(t, 1, f.sends)
}
