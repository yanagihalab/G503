package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
)

func (l *safetyLab) operatorCLI(action, id, evidence string, rejectStale bool) {
	l.t.Helper()
	authority := l.keys[0]["authority"]
	keyFile := filepath.Join(l.home, "authority-operator.key")
	require.NoError(l.t, os.WriteFile(keyFile, []byte(authority.Key), 0600))
	account, err := l.rpc.AccountAt(l.ctx, authority.Address, 0)
	require.NoError(l.t, err)
	endpoints := strings.Split(os.Getenv("G503_OPERATOR_RPC_ENDPOINTS"), ",")
	require.Len(l.t, endpoints, 5)
	endpoints[0] = "http://127.0.0.1:1"
	args := []string{"run", "./dev/agent/cmd/g503-ops", action, "-chain-id", l.chain, "-rpc-url", "http://127.0.0.1:1", "-rpc-endpoints", strings.Join(endpoints, ","), "-rpc-min-agreement", "3", "-rpc-state-dir", filepath.Join(l.home, "authority-rpc"), "-key-file", keyFile, "-case-id", id, "-reason", "operator accepts signed review evidence", "-account-number", fmt.Sprint(account.Number), "-sequence", fmt.Sprint(account.Sequence), "-request-id", action + "-" + id}
	if evidence != "" {
		args = append(args, "-evidence-hash-hex", evidence)
	}
	dir, err := filepath.Abs("../../..")
	require.NoError(l.t, err)
	if rejectStale {
		var sends atomic.Int32
		stale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID     uint64
				Method string
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Method == "broadcast_tx_sync" {
				sends.Add(1)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"node_info": map[string]string{"network": l.chain}, "sync_info": map[string]any{"latest_block_height": "1", "latest_block_hash": strings.Repeat("AA", 32), "latest_block_time": time.Now().Add(-time.Minute), "catching_up": false}}})
		}))
		defer stale.Close()
		bad := append([]string{}, args...)
		bad = append(bad, "-rpc-endpoints", stale.URL, "-rpc-min-agreement", "1")
		cmd := exec.CommandContext(l.ctx, "go", bad...)
		cmd.Dir = dir
		output, err := cmd.CombinedOutput()
		require.Error(l.t, err)
		require.Contains(l.t, string(output), "stale or future")
		require.Zero(l.t, sends.Load())
		after, err := l.rpc.AccountAt(l.ctx, authority.Address, 0)
		require.NoError(l.t, err)
		require.Equal(l.t, account.Sequence, after.Sequence)
	}
	cmd := exec.CommandContext(l.ctx, "go", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoError(l.t, err, string(output))
	var result struct {
		Broadcast chainclient.BroadcastResult `json:"broadcast"`
	}
	require.NoError(l.t, json.Unmarshal(output, &result))
	require.Zero(l.t, result.Broadcast.Code)
	require.Eventually(l.t, func() bool {
		tx, err := l.rpc.LookupTx(l.ctx, result.Broadcast.Hash)
		return err == nil && tx.Found && tx.Code == 0
	}, 30*time.Second, 150*time.Millisecond)
	l.save("operator-"+action+".json", map[string]any{"hash": result.Broadcast.Hash, "rpc_failover": true, "stale_rejected": rejectStale})
}
