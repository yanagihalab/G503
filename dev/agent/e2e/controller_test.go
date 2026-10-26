package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	bank "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	"github.com/yanagihalab/G503/dev/agent/internal/controller"
	"github.com/yanagihalab/G503/dev/agent/internal/rpcguard"
	st "github.com/yanagihalab/G503/x/sanction/types"
)

func TestCompoundControllerRecovery(t *testing.T) {
	if os.Getenv("G503_COMPOUND_RPC_ENDPOINTS") == "" {
		t.Skip("run the five-agent fixture")
	}
	l := lab(t)
	dir := filepath.Join(l.home, "compound-controller")
	require.NoError(t, os.MkdirAll(dir, 0700))
	genesis, err := l.rpc.GenesisHash(l.ctx, l.chain)
	require.NoError(t, err)
	identity := map[string]string{"chain": l.chain, "genesis": genesis, "scenario": "compound-controller-v1"}
	role := os.Getenv("G503_COMPOUND_CONTROLLER_CHILD")
	if role != "" {
		j, err := controller.Open(filepath.Join(dir, "controller.db"), identity, role == "resume")
		require.NoError(t, err)
		defer j.Close()
		var hash string
		err = j.Step(l.ctx, "suspicious-transfer", &hash, func(ctx context.Context, id string) (any, error) {
			faucet, demo := l.keys[0]["faucet"], l.keys[0]["demo"]
			policy := chainclient.RPCSafety{GenesisHash: genesis, MaxHeightLag: 2, MinAgreement: 3, OnBroadcast: func(chainclient.Checkpoint, string) {
				f, err := os.OpenFile(filepath.Join(dir, "broadcasts.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				require.NoError(t, err)
				_, err = f.WriteString("send\n")
				require.NoError(t, err)
				require.NoError(t, f.Close())
			}}
			g, err := rpcguard.Open(l.chain, l.rpcURL, rpcguard.Options{StateDir: filepath.Join(dir, "rpc"), Namespace: faucet.Address, Endpoints: os.Getenv("G503_COMPOUND_RPC_ENDPOINTS"), Policy: policy})
			require.NoError(t, err)
			defer g.Close()
			height, err := g.Client.Status(ctx)
			require.NoError(t, err)
			a, err := g.Client.AccountAt(ctx, faucet.Address, height.Height)
			require.NoError(t, err)
			r, err := g.Send(ctx, id, chainclient.SignOptions{ChainID: l.chain, PrivateKeyHex: faucet.Key, AccountNumber: a.Number, Sequence: a.Sequence, GasLimit: 200000}, &bank.MsgSend{FromAddress: faucet.Address, ToAddress: demo.Address, Amount: sdk.NewCoins(sdk.NewInt64Coin("stake", 3))})
			require.NoError(t, err)
			if role == "crash" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "accepted-hash"), []byte(r.Hash), 0600))
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return r.Hash, nil
		})
		require.NoError(t, err)
		require.NoError(t, j.Finish("passed"))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "resumed-hash"), []byte(hash), 0600))
		return
	}
	initialFrom, initialTo := l.balance(l.keys[0]["faucet"].Address), l.balance(l.keys[0]["demo"].Address)
	start := func(role string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCompoundControllerRecovery$", "-test.v")
		cmd.Env = append(os.Environ(), "G503_COMPOUND_CONTROLLER_CHILD="+role)
		f, err := os.OpenFile(filepath.Join(dir, role+".log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		require.NoError(t, err)
		t.Cleanup(func() { _ = f.Close() })
		cmd.Stdout, cmd.Stderr = f, f
		require.NoError(t, cmd.Start())
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		return cmd
	}
	crashed := start("crash")
	var accepted []byte
	require.Eventually(t, func() bool {
		accepted, _ = os.ReadFile(filepath.Join(dir, "accepted-hash"))
		return len(accepted) == 64
	}, 45*time.Second, 100*time.Millisecond)
	_, err = controller.Open(filepath.Join(dir, "controller.db"), identity, true)
	require.Error(t, err, "a live controller must remain exclusive")
	require.NoError(t, crashed.Process.Kill())
	require.Error(t, crashed.Wait())
	var tx chainclient.TxResult
	require.Eventually(t, func() bool {
		tx, err = l.rpc.LookupTx(l.ctx, string(accepted))
		return err == nil && tx.Found && tx.Code == 0
	}, 30*time.Second, 100*time.Millisecond)
	resumed := start("resume")
	require.NoError(t, resumed.Wait())
	actual, err := os.ReadFile(filepath.Join(dir, "resumed-hash"))
	require.NoError(t, err)
	require.Equal(t, accepted, actual)
	sends, err := os.ReadFile(filepath.Join(dir, "broadcasts.log"))
	require.NoError(t, err)
	require.Equal(t, 1, bytes.Count(sends, []byte("send\n")))
	caseID := "auto-" + strings.ToUpper(string(accepted))
	require.Eventually(t, func() bool {
		r := &st.QueryExecutionRecordResponse{}
		return l.query(l.rpc, "ExecutionRecord", &st.QueryExecutionRecordRequest{CaseId: caseID}, r) == nil && r.Record != nil && r.Record.ResultCode == "ok"
	}, 90*time.Second, 200*time.Millisecond)
	settlements := &st.QuerySettlementsResponse{}
	require.NoError(t, l.query(l.rpc, "Settlements", &st.QuerySettlementsRequest{TxHash: strings.ToUpper(string(accepted))}, settlements))
	require.Len(t, settlements.Settlements, 1)
	votes := &st.QuerySanctionVotesResponse{}
	require.NoError(t, l.query(l.rpc, "SanctionVotes", &st.QuerySanctionVotesRequest{CaseId: caseID}, votes))
	require.Len(t, votes.Votes, 5)
	require.Eventually(t, func() bool {
		for i := 1; i <= 5; i++ {
			name := filepath.Join(l.home, "logs", fmt.Sprintf("agent%d-second.jsonl", i))
			bz, err := os.ReadFile(name)
			if err != nil {
				return false
			}
			done := false
			for _, line := range bytes.Split(bz, []byte("\n")) {
				var e map[string]any
				if json.Unmarshal(line, &e) == nil && e["event"] == "complete" && e["case_id"] == caseID {
					done = true
				}
			}
			if !done {
				return false
			}
		}
		return true
	}, 45*time.Second, 200*time.Millisecond)
	require.Equal(t, initialFrom, l.balance(l.keys[0]["faucet"].Address))
	require.Equal(t, initialTo, l.balance(l.keys[0]["demo"].Address))
	l.save("compound-controller-metrics.json", map[string]any{"validators": 5, "agents": 5, "controller_sigkill_resumed": true, "broadcasts": 1, "unique_settlements": 1, "votes": 5, "balances_restored": true, "rpc_endpoints": 5})
}
