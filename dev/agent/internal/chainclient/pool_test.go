package chainclient

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type rpcFixture struct {
	mu                        sync.Mutex
	height                    uint64
	chain, branch             string
	age                       time.Duration
	catchingUp, down, loseACK bool
	broadcasts                int
	omitSync                  bool
	server                    *httptest.Server
}

func blockHash(branch string, height uint64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", branch, height)))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func newRPCFixture(t *testing.T) *rpcFixture {
	t.Helper()
	f := &rpcFixture{height: 10, chain: "test", branch: "main"}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down {
			http.Error(w, "offline", 503)
			return
		}
		var req struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
			Params struct {
				Height string `json:"height"`
				Tx     string `json:"tx"`
				Limit  string `json:"limit"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad JSON", 400)
			return
		}
		var result any
		switch req.Method {
		case "status":
			result = map[string]any{"node_info": map[string]string{"network": f.chain}, "sync_info": map[string]any{
				"latest_block_height": strconv.FormatUint(f.height, 10), "latest_block_hash": blockHash(f.branch, f.height),
				"latest_block_time": time.Now().Add(-f.age), "catching_up": f.catchingUp,
			}}
			if f.omitSync {
				delete(result.(map[string]any)["sync_info"].(map[string]any), "catching_up")
			}
		case "genesis":
			result = map[string]any{"genesis": map[string]any{"chain_id": f.chain, "genesis_time": "2026-10-26T00:00:00Z", "app_state": map[string]any{"value": 1}}}
		case "block":
			h, _ := strconv.ParseUint(req.Params.Height, 10, 64)
			if h > f.height {
				http.Error(w, "future height", 500)
				return
			}
			result = map[string]any{"block_id": map[string]string{"hash": blockHash(f.branch, h)}, "block": map[string]any{"header": map[string]any{
				"chain_id": f.chain, "height": req.Params.Height, "time": time.Now().Add(-f.age),
			}}}
		case "broadcast_tx_sync":
			f.broadcasts++
			if f.loseACK {
				http.Error(w, "lost acknowledgement", 503)
				return
			}
			tx, _ := base64.StdEncoding.DecodeString(req.Params.Tx)
			sum := sha256.Sum256(tx)
			result = map[string]any{"code": 0, "hash": hex.EncodeToString(sum[:])}
		case "unconfirmed_txs":
			if req.Params.Limit == "" {
				http.Error(w, "CometBFT requires a string limit", 400)
				return
			}
			result = map[string]any{"txs": [][]byte{[]byte("pending")}}
		default:
			http.Error(w, "unexpected method", 400)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
			t.Errorf("response: %v", err)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func fixturePool(t *testing.T, fixtures ...*rpcFixture) *Client {
	t.Helper()
	endpoints := make([]string, len(fixtures))
	for n, f := range fixtures {
		endpoints[n] = f.server.URL
	}
	c, err := NewResilient(endpoints, time.Second, RPCSafety{ChainID: "test", MaxHeightLag: 2, ProbeTimeout: time.Second})
	require.NoError(t, err)
	return c
}

func TestRPCFreshnessRejectsUnsafeEndpointsBeforeSend(t *testing.T) {
	for _, scenario := range []string{"stale", "future", "catching-up", "missing-sync-state", "wrong-chain", "rollback", "fork", "persist-error"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRPCFixture(t)
			c := fixturePool(t, f)
			_, err := c.Status(context.Background())
			require.NoError(t, err)
			cp, _ := c.RPCObservation()
			f.mu.Lock()
			switch scenario {
			case "stale":
				f.age = time.Minute
			case "future":
				f.age = -time.Minute
			case "catching-up":
				f.catchingUp = true
			case "missing-sync-state":
				f.omitSync = true
			case "wrong-chain":
				f.chain = "other"
			case "rollback":
				f.height = cp.Height - 1
			case "fork":
				f.branch = "fork"
				f.height++
			case "persist-error":
				f.height++
			}
			f.mu.Unlock()
			if scenario == "persist-error" {
				c.pool.policy.Persist = func(Checkpoint) error { return fmt.Errorf("disk full") }
			}
			_, err = c.BroadcastTxSync(context.Background(), []byte("tx"))
			require.Error(t, err)
			f.mu.Lock()
			defer f.mu.Unlock()
			require.Zero(t, f.broadcasts)
		})
	}
}

func TestRPCFailoverPinsCommonHistoryAndRetainsEndpointCount(t *testing.T) {
	a, b, c := newRPCFixture(t), newRPCFixture(t), newRPCFixture(t)
	pool := fixturePool(t, a, b, c)
	status, err := pool.Status(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 10, status.Height)
	first, _ := pool.RPCObservation()
	a.mu.Lock()
	a.down = true
	a.mu.Unlock()
	for _, f := range []*rpcFixture{b, c} {
		f.mu.Lock()
		f.height = 11
		f.mu.Unlock()
	}
	status, err = pool.Status(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 11, status.Height)
	_, endpoint := pool.RPCObservation()
	require.NotEqual(t, a.server.URL, endpoint)
	_, err = pool.BroadcastTxSync(context.Background(), []byte("tx"))
	require.NoError(t, err)
	pending, err := pool.PendingTransactions(context.Background(), 5)
	require.NoError(t, err)
	require.Equal(t, [][]byte{[]byte("pending")}, pending)
	b.mu.Lock()
	b.down = true
	b.mu.Unlock()
	_, err = pool.Status(context.Background())
	require.Error(t, err, "one survivor must not lower the original majority")
	restored, err := NewResilient([]string{c.server.URL}, time.Second, RPCSafety{ChainID: "test", Checkpoint: first})
	require.NoError(t, err)
	c.mu.Lock()
	c.branch = "reset-with-same-genesis"
	c.height = 12
	c.mu.Unlock()
	_, err = restored.Status(context.Background())
	require.Error(t, err, "same genesis cannot waive checkpoint continuity after restart")
}

func TestRPCRejectsConflictingWitnessesAndGenesisMismatch(t *testing.T) {
	a, b := newRPCFixture(t), newRPCFixture(t)
	b.branch = "fork"
	c, err := NewResilient([]string{a.server.URL, b.server.URL}, time.Second, RPCSafety{ChainID: "test", MinAgreement: 1})
	require.NoError(t, err)
	_, err = c.Status(context.Background())
	require.ErrorContains(t, err, "conflicting RPC groups")
	c, err = NewResilient([]string{a.server.URL}, time.Second, RPCSafety{ChainID: "test", GenesisHash: strings.Repeat("00", 32)})
	require.NoError(t, err)
	_, err = c.Status(context.Background())
	require.Error(t, err)
}

func TestRPCDoesNotReplayAmbiguousBroadcastOnAnotherEndpoint(t *testing.T) {
	a, b, c := newRPCFixture(t), newRPCFixture(t), newRPCFixture(t)
	a.loseACK = true
	pool := fixturePool(t, a, b, c)
	_, err := pool.BroadcastTxSync(context.Background(), []byte("tx"))
	require.Error(t, err)
	require.Equal(t, 1, a.broadcasts)
	require.Zero(t, b.broadcasts)
	require.Zero(t, c.broadcasts)
	// Selection on the next tick is safe; replay is left exclusively to the journal.
	a.mu.Lock()
	a.down = true
	a.mu.Unlock()
	_, err = pool.Status(context.Background())
	require.NoError(t, err)
	require.Zero(t, b.broadcasts)
	require.Zero(t, c.broadcasts)
}

func TestRPCCommonHeightAndConcurrentQueries(t *testing.T) {
	a, b, c := newRPCFixture(t), newRPCFixture(t), newRPCFixture(t)
	a.height = 12
	b.height = 11
	c.height = 10
	pool := fixturePool(t, a, b, c)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, err := pool.Status(context.Background())
			if err == nil && status.Height != 10 {
				err = fmt.Errorf("common height=%d", status.Height)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func TestRPCConfigurationFailsClosed(t *testing.T) {
	for _, endpoints := range [][]string{nil, {"file:///tmp/node"}, {"http://user:password@node"}, {"http://node?key=secret"}, {"http://node", "http://node/"}} {
		_, err := NewResilient(endpoints, time.Second, RPCSafety{ChainID: "test"})
		require.Error(t, err)
	}
	_, err := NewResilient([]string{"http://node"}, time.Second, RPCSafety{ChainID: "test", MinAgreement: 2})
	require.Error(t, err)
	_, err = NewResilient([]string{"http://node"}, time.Second, RPCSafety{ChainID: "test", Checkpoint: Checkpoint{Height: 1}})
	require.Error(t, err)
}

func TestRPCSlowCheckpointPersistenceStopsBroadcast(t *testing.T) {
	f := newRPCFixture(t)
	var saved Checkpoint
	c, err := NewResilient([]string{f.server.URL}, time.Second, RPCSafety{
		ChainID: "test", MaxBlockAge: 500 * time.Millisecond,
		Persist: func(cp Checkpoint) error {
			saved = cp
			time.Sleep(600 * time.Millisecond)
			return nil
		},
	})
	require.NoError(t, err)
	_, err = c.BroadcastTxSync(context.Background(), []byte("tx"))
	require.ErrorContains(t, err, "expired while persisting")
	cp, endpoint := c.RPCObservation()
	require.Equal(t, saved, cp, "memory must retain the checkpoint already persisted on disk")
	require.Empty(t, endpoint)
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Zero(t, f.broadcasts)
}
