package ownership

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	st "github.com/yanagihalab/G503/x/sanction/types"
	"google.golang.org/protobuf/proto"
)

func TestValidationFailsClosedAndNeverReacquiresForOldProcess(t *testing.T) {
	for _, scenario := range []string{"valid", "expired", "replaced", "absent", "RPC-error", "wrong-chain"} {
		t.Run(scenario, func(t *testing.T) {
			requests := 0
			holder := strings.Repeat("a", 64)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ID     uint64 `json:"id"`
					Method string `json:"method"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				requests++
				var result any
				switch req.Method {
				case "status":
					chain := "test"
					if scenario == "wrong-chain" {
						chain = "other"
					}
					result = map[string]any{"node_info": map[string]string{"network": chain}, "sync_info": map[string]string{"latest_block_height": "10"}}
				case "abci_query":
					if scenario == "RPC-error" {
						w.WriteHeader(503)
						return
					}
					lease := &st.AgentLease{SignerAddress: "address", HolderId: holder, Epoch: 1, ExpiresHeight: 11}
					if scenario == "expired" {
						lease.ExpiresHeight = 10
					}
					if scenario == "replaced" {
						lease.Epoch = 2
					}
					if scenario == "absent" {
						lease = nil
					}
					bz, _ := proto.Marshal(&st.QueryAgentLeaseResponse{Lease: lease})
					result = map[string]any{"response": map[string]any{"code": 0, "height": "10", "value": bz}}
				default:
					t.Errorf("validation must not sign/acquire/broadcast: %s", req.Method)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			}))
			defer server.Close()
			s := &Session{RPC: chainclient.New(server.URL, time.Second), Address: "address", holder: holder, Lease: &st.AgentLease{Epoch: 1, HolderId: holder, ExpiresHeight: 11}, Options: chainclient.SignOptions{ChainID: "test"}}
			err := s.Validate(context.Background())
			if scenario == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.LessOrEqual(t, requests, 2)
		})
	}
}

func TestEnsureReadsOnePinnedSnapshotWithoutCachingOwnership(t *testing.T) {
	holder := strings.Repeat("a", 64)
	var epoch atomic.Uint64
	epoch.Store(1)
	var mu sync.Mutex
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
			Params struct {
				Height string `json:"height"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode RPC request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		methods = append(methods, request.Method)
		mu.Unlock()
		var result any
		switch request.Method {
		case "status":
			result = map[string]any{"node_info": map[string]string{"network": "test"}, "sync_info": map[string]string{"latest_block_height": "10"}}
		case "abci_query":
			if request.Params.Height != "10" {
				t.Errorf("lease query height = %q, want 10", request.Params.Height)
			}
			value, err := proto.Marshal(&st.QueryAgentLeaseResponse{Lease: &st.AgentLease{SignerAddress: "address", HolderId: holder, Epoch: epoch.Load(), ExpiresHeight: 100}})
			if err != nil {
				t.Errorf("encode lease: %v", err)
				http.Error(w, "encoding failed", http.StatusInternalServerError)
				return
			}
			result = map[string]any{"response": map[string]any{"code": 0, "height": "10", "value": value}}
		default:
			t.Errorf("unexpected RPC: %s", request.Method)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			t.Errorf("encode RPC response: %v", err)
		}
	}))
	defer server.Close()
	s := &Session{RPC: chainclient.New(server.URL, time.Second), Address: "address", holder: holder, TTL: 24,
		Lease: &st.AgentLease{Epoch: 1, HolderId: holder, ExpiresHeight: 100}, Options: chainclient.SignOptions{ChainID: "test"}}
	require.NoError(t, s.Ensure(context.Background()))
	epoch.Store(2)
	require.ErrorIs(t, s.Ensure(context.Background()), ErrLost)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"status", "abci_query", "status", "abci_query"}, methods)
}
