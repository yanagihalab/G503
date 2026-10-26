package txmanager

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cometbft/cometbft/crypto/tmhash"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	st "github.com/yanagihalab/G503/x/sanction/types"
)

type fakeChain struct {
	sequence        uint64
	results         map[string]chainclient.TxResult
	sent            [][]byte
	loseACK         bool
	lookupErr       error
	checkCode       uint32
	applicationCode uint32
}

func (f *fakeChain) AccountAt(context.Context, string, uint64) (chainclient.Account, error) {
	return chainclient.Account{Number: 4, Sequence: f.sequence}, nil
}
func (f *fakeChain) LookupTx(_ context.Context, hash string) (chainclient.TxResult, error) {
	return f.results[hash], f.lookupErr
}
func (f *fakeChain) BroadcastTxSync(_ context.Context, bz []byte) (chainclient.BroadcastResult, error) {
	f.sent = append(f.sent, append([]byte(nil), bz...))
	hash := strings.ToUpper(hex.EncodeToString(tmhash.Sum(bz)))
	if f.checkCode != 0 {
		return chainclient.BroadcastResult{Code: f.checkCode}, fmt.Errorf("CheckTx failure")
	}
	f.results[hash] = chainclient.TxResult{Found: true, Height: 2, Code: f.applicationCode}
	f.sequence++
	if f.loseACK {
		return chainclient.BroadcastResult{}, io.ErrUnexpectedEOF
	}
	return chainclient.BroadcastResult{Hash: hash}, nil
}

func newTestManager(store *Store, chain *fakeChain) *Manager {
	return &Manager{Store: store, Chain: chain, Address: "test-address", RetryInterval: -time.Second, Options: chainclient.SignOptions{ChainID: "test-chain", PrivateKeyHex: strings.Repeat("01", 32), FeeAmount: sdk.NewCoins()}, Satisfied: func(context.Context, Intent, uint64) (bool, error) { return false, nil }}
}
func enqueue(t *testing.T, m *Manager) {
	t.Helper()
	require.NoError(t, m.Enqueue("execute/auto-test", "execute", "auto-test", &st.MsgExecuteSanction{Executor: "g50310xcqpzrky6eff2g52qdye53xkk9jxkvrnsxwnj", CaseId: "auto-test"}))
}
func readIntent(t *testing.T, s *Store) Intent {
	t.Helper()
	var i Intent
	found, err := s.Get("intents", "execute/auto-test", &i)
	require.NoError(t, err)
	require.True(t, found)
	return i
}

func TestCrashAfterSigningBeforeBroadcastResendsIdenticalBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	s, err := Open(path, "identity")
	require.NoError(t, err)
	f := &fakeChain{results: map[string]chainclient.TxResult{}}
	m := newTestManager(s, f)
	enqueue(t, m)
	m.OnEvent = func(i Intent) {
		if i.Status == "pending" {
			panic("simulated power loss")
		}
	}
	require.Panics(t, func() { _ = m.Step(context.Background(), 1) })
	signed := readIntent(t, s).Attempts[0].Tx
	require.Empty(t, f.sent)
	require.NoError(t, s.Close())
	s, err = Open(path, "identity")
	require.NoError(t, err)
	defer s.Close()
	m = newTestManager(s, f)
	require.NoError(t, m.Step(context.Background(), 1))
	require.Equal(t, signed, f.sent[0])
	require.NoError(t, m.Step(context.Background(), 2))
	require.Equal(t, "confirmed", readIntent(t, s).Status)
	require.Len(t, readIntent(t, s).Attempts, 1)
}

func TestLostACKThenRestartConfirmsWithoutRebroadcast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	s, err := Open(path, "identity")
	require.NoError(t, err)
	f := &fakeChain{results: map[string]chainclient.TxResult{}, loseACK: true}
	m := newTestManager(s, f)
	enqueue(t, m)
	require.ErrorIs(t, m.Step(context.Background(), 1), io.ErrUnexpectedEOF)
	require.Equal(t, "pending", readIntent(t, s).Status)
	require.NoError(t, s.Close())
	s, err = Open(path, "identity")
	require.NoError(t, err)
	defer s.Close()
	m = newTestManager(s, f)
	enqueue(t, m)
	require.NoError(t, m.Step(context.Background(), 2))
	require.NoError(t, m.Step(context.Background(), 3))
	require.Len(t, f.sent, 1)
	require.Equal(t, "confirmed", readIntent(t, s).Status)
	require.Equal(t, uint64(2), readIntent(t, s).CompletedHeight)
}

func TestLookupFailureIsNotTreatedAsNotFound(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "agent.db"), "identity")
	require.NoError(t, err)
	defer s.Close()
	f := &fakeChain{results: map[string]chainclient.TxResult{}, loseACK: true}
	m := newTestManager(s, f)
	enqueue(t, m)
	require.Error(t, m.Step(context.Background(), 1))
	f.lookupErr = io.ErrUnexpectedEOF
	require.ErrorIs(t, m.Step(context.Background(), 2), io.ErrUnexpectedEOF)
	require.Len(t, f.sent, 1)
}

func TestSemanticReconciliationAndExternalSequenceAdvance(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "agent.db"), "identity")
	require.NoError(t, err)
	defer s.Close()
	f := &fakeChain{results: map[string]chainclient.TxResult{}, loseACK: true}
	m := newTestManager(s, f)
	enqueue(t, m)
	require.Error(t, m.Step(context.Background(), 1))
	f.results = map[string]chainclient.TxResult{}
	f.loseACK = false
	require.NoError(t, m.Step(context.Background(), 2))
	i := readIntent(t, s)
	require.Len(t, i.Attempts, 2)
	require.Equal(t, uint64(1), i.Attempts[1].Sequence)
	m.Satisfied = func(context.Context, Intent, uint64) (bool, error) { return true, nil }
	f.results = map[string]chainclient.TxResult{}
	require.NoError(t, m.Step(context.Background(), 3))
	require.Equal(t, "satisfied", readIntent(t, s).Status)
	require.Len(t, f.sent, 2)
}

func TestDefiniteFailuresAreDurableAndNotRetriedForever(t *testing.T) {
	for _, check := range []bool{true, false} {
		t.Run(fmt.Sprint(check), func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "agent.db"), "identity")
			require.NoError(t, err)
			defer s.Close()
			f := &fakeChain{results: map[string]chainclient.TxResult{}}
			if check {
				f.checkCode = 11
			} else {
				f.applicationCode = 11
			}
			m := newTestManager(s, f)
			enqueue(t, m)
			_ = m.Step(context.Background(), 1)
			require.NoError(t, m.Step(context.Background(), 2))
			require.Equal(t, "failed", readIntent(t, s).Status)
			require.NoError(t, m.Step(context.Background(), 3))
			require.Len(t, f.sent, 1)
		})
	}
}

func TestJournalExclusiveIdentityAndConcurrentSteps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	s, err := Open(path, "identity")
	require.NoError(t, err)
	_, err = Open(path, "identity")
	require.Error(t, err)
	f := &fakeChain{results: map[string]chainclient.TxResult{}}
	m := newTestManager(s, f)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); enqueue(t, m); require.NoError(t, m.Step(context.Background(), 2)) }()
	}
	wg.Wait()
	require.Len(t, f.sent, 1)
	require.Equal(t, "confirmed", readIntent(t, s).Status)
	require.NoError(t, s.Close())
	_, err = Open(path, "another-chain-or-signer")
	require.Error(t, err)
}

func TestPendingBackoffAndObsoleteIntent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "agent.db"), "identity")
	require.NoError(t, err)
	defer s.Close()
	f := &fakeChain{results: map[string]chainclient.TxResult{}, loseACK: true}
	m := newTestManager(s, f)
	m.RetryInterval = time.Hour
	enqueue(t, m)
	require.Error(t, m.Step(context.Background(), 1))
	f.results = map[string]chainclient.TxResult{}
	f.sequence = 0
	require.NoError(t, m.Step(context.Background(), 1))
	require.Len(t, f.sent, 1)
	m.Satisfied = func(context.Context, Intent, uint64) (bool, error) { return false, ErrObsolete }
	require.NoError(t, m.Step(context.Background(), 2))
	require.Equal(t, "cancelled", readIntent(t, s).Status)
	require.Len(t, f.sent, 1)
	require.Error(t, m.Enqueue("execute/auto-test", "execute", "different-case", &st.MsgExecuteSanction{}))
}
