package chainclient

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/stretchr/testify/require"
)

func TestLookupTxStrictAbsenceAndIndexedBytes(t *testing.T) {
	bz := []byte("indexed transaction")
	hash := strings.ToUpper(hex.EncodeToString(tmhash.Sum(bz)))
	for _, scenario := range []string{"success", "absent", "index-disabled", "unrelated-not-found", "wrong-bytes", "missing-code"} {
		t.Run(scenario, func(t *testing.T) {
			c := New("http://node.test", time.Second)
			c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				result := map[string]any{"hash": hash, "height": "12", "tx": bz, "tx_result": map[string]any{"code": 0}}
				response := map[string]any{"jsonrpc": "2.0", "id": 1, "result": result}
				switch scenario {
				case "absent":
					response["error"] = map[string]any{"code": -32603, "message": "Internal error", "data": fmt.Sprintf("tx (%s) not found", hash)}
				case "index-disabled":
					response["error"] = map[string]any{"code": -32603, "message": "Internal error", "data": "transaction indexing is disabled"}
				case "unrelated-not-found":
					response["error"] = map[string]any{"code": -32603, "message": "Internal error", "data": "storage file not found"}
				case "wrong-bytes":
					result["tx"] = []byte("different transaction")
				case "missing-code":
					result["tx_result"] = map[string]any{}
				}
				body, err := json.Marshal(response)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}, err
			})}
			result, err := c.LookupTx(context.Background(), hash)
			if scenario == "absent" {
				require.NoError(t, err)
				require.False(t, result.Found)
			} else if scenario == "success" {
				require.NoError(t, err)
				require.True(t, result.Found)
				require.Equal(t, bz, result.Tx)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestQueryUsesHexWireFormatAndPinsHeight(t *testing.T) {
	for _, height := range []string{"12", "11"} {
		t.Run(height, func(t *testing.T) {
			c := New("http://node.test", time.Second)
			c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var request struct {
					Params struct {
						Data   string `json:"data"`
						Height string `json:"height"`
					} `json:"params"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				require.Equal(t, "010203", request.Params.Data)
				require.Equal(t, "12", request.Params.Height)
				body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"response": map[string]any{"code": 0, "height": height, "value": []byte("response")}}})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}, err
			})}
			bz, err := c.Query(context.Background(), "/query", []byte{1, 2, 3}, 12)
			if height == "12" {
				require.NoError(t, err)
				require.Equal(t, []byte("response"), bz)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestGenesisBindingCanonicalizationAndResetDetection(t *testing.T) {
	var hashes []string
	for _, genesis := range []string{
		`{"chain_id":"test","genesis_time":"2026-10-23T00:00:00Z","app_state":{"value":9007199254740993}}`,
		`{ "app_state": { "value":9007199254740993 }, "genesis_time":"2026-10-23T00:00:00Z", "chain_id":"test" }`,
		`{"chain_id":"test","genesis_time":"2026-10-23T00:00:01Z","app_state":{"value":9007199254740993}}`,
	} {
		c := New("http://node.test", time.Second)
		c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var request struct {
				ID uint64 `json:"id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				return nil, err
			}
			body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"genesis": json.RawMessage(genesis)}})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}, err
		})}
		hash, err := c.GenesisHash(context.Background(), "test")
		require.NoError(t, err)
		hashes = append(hashes, hash)
		_, err = c.GenesisHash(context.Background(), "wrong")
		require.Error(t, err)
	}
	require.Equal(t, hashes[0], hashes[1])
	require.NotEqual(t, hashes[0], hashes[2])
}
