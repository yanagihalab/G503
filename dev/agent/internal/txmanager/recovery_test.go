package txmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	st "github.com/yanagihalab/G503/x/sanction/types"
)

func retryMessages(Intent) ([]sdk.Msg, error) {
	return []sdk.Msg{&st.MsgExecuteSanction{Executor: "g50310xcqpzrky6eff2g52qdye53xkk9jxkvrnsxwnj", CaseId: "auto-test"}}, nil
}

func TestExplicitRetryKeepsHistoryAndUsesCurrentSequence(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "agent.db"), "identity")
	require.NoError(t, err)
	defer s.Close()
	f := &fakeChain{results: map[string]chainclient.TxResult{}, applicationCode: 11}
	m := newTestManager(s, f)
	enqueue(t, m)
	require.NoError(t, m.Step(context.Background(), 1))
	require.NoError(t, m.Step(context.Background(), 2))
	require.Equal(t, "failed", readIntent(t, s).Status)
	f.applicationCode = 0
	require.NoError(t, m.Recover(context.Background(), "execute/auto-test", 2, "gas repaired", retryMessages))
	require.Equal(t, "queued", readIntent(t, s).Status)
	require.NoError(t, m.Step(context.Background(), 3))
	require.NoError(t, m.Step(context.Background(), 3))
	i := readIntent(t, s)
	require.Equal(t, "confirmed", i.Status)
	require.Len(t, i.Attempts, 2)
	require.Len(t, i.Recoveries, 1)
	require.Equal(t, uint64(1), i.Attempts[1].Sequence)
	require.NoError(t, m.Step(context.Background(), 4))
	require.Len(t, f.sent, 2)
	require.NoError(t, m.Recover(context.Background(), "execute/auto-test", 4, "gas repaired", func(Intent) ([]sdk.Msg, error) { t.Fatal("replayed request must not prepare again"); return nil, nil }))
	require.Len(t, readIntent(t, s).Recoveries, 1)
}

func TestRecoveryReconcilesBeforePrepareAndFailsClosed(t *testing.T) {
	for _, scenario := range []string{"satisfied", "lookup-error", "uncertain", "unauthorized", "check-rejected"} {
		t.Run(scenario, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "agent.db"), "identity")
			require.NoError(t, err)
			defer s.Close()
			f := &fakeChain{results: map[string]chainclient.TxResult{}, checkCode: 11}
			m := newTestManager(s, f)
			enqueue(t, m)
			require.Error(t, m.Step(context.Background(), 1))
			switch scenario {
			case "satisfied":
				m.Satisfied = func(context.Context, Intent, uint64) (bool, error) { return true, nil }
			case "lookup-error":
				f.lookupErr = fmt.Errorf("offline")
			case "uncertain":
				i := readIntent(t, s)
				i.Attempts[0].CheckCode = 0
				i.Attempts[0].Code = 11
				require.NoError(t, s.Put("intents", i.ID, i))
			case "unauthorized":
				i := readIntent(t, s)
				i.Attempts[0].CheckCode = 4
				require.NoError(t, s.Put("intents", i.ID, i))
			}
			called := false
			err = m.Recover(context.Background(), "execute/auto-test", 2, "repair", func(i Intent) ([]sdk.Msg, error) { called = true; return retryMessages(i) })
			if scenario == "satisfied" {
				require.NoError(t, err)
				require.False(t, called)
				require.Equal(t, "satisfied", readIntent(t, s).Status)
			} else if scenario == "check-rejected" {
				require.NoError(t, err)
				require.True(t, called)
				require.Equal(t, "queued", readIntent(t, s).Status)
			} else {
				require.Error(t, err)
				require.False(t, called)
				require.Equal(t, "failed", readIntent(t, s).Status)
			}
			require.Len(t, f.sent, 1)
		})
	}
}

func TestBackupRestoreAndStaleFailedJournalReconciliation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.db")
	genesis := strings.Repeat("ab", 32)
	s, err := Open(path, "identity")
	require.NoError(t, err)
	require.NoError(t, s.BindGenesis(genesis, false))
	f := &fakeChain{results: map[string]chainclient.TxResult{}, checkCode: 11}
	m := newTestManager(s, f)
	enqueue(t, m)
	require.Error(t, m.Step(context.Background(), 1))
	_, err = Backup(path, filepath.Join(dir, "live.tar"))
	require.Error(t, err, "must not back up a running journal")
	require.NoError(t, s.Close())
	archive := filepath.Join(dir, "journal.tar")
	result, err := Backup(path, archive)
	require.NoError(t, err)
	_, err = Backup(path, archive)
	require.Error(t, err, "must not overwrite backups")
	restored := filepath.Join(dir, "restored", "agent.db")
	require.Error(t, Restore(archive, restored, "wrong", genesis, result.SHA256))
	require.Error(t, Restore(archive, restored, "identity", strings.Repeat("cd", 32), result.SHA256))
	require.Error(t, Restore(archive, restored, "identity", genesis, strings.Repeat("00", 32)))
	require.NoError(t, Restore(archive, restored, "identity", genesis, result.SHA256))
	require.Error(t, Restore(archive, restored, "identity", genesis, result.SHA256))
	info, err := os.Stat(restored)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	s, err = Open(restored, "identity")
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.BindGenesis(genesis, false))
	require.Error(t, s.BindGenesis(strings.Repeat("cd", 32), true))
	m = newTestManager(s, f)
	m.Satisfied = func(context.Context, Intent, uint64) (bool, error) { return true, nil }
	require.NoError(t, m.Step(context.Background(), 5))
	require.Equal(t, "satisfied", readIntent(t, s).Status)
	require.Len(t, f.sent, 1)
	bz, err := os.ReadFile(archive)
	require.NoError(t, err)
	bz[len(bz)/2] ^= 0xff
	corrupt := filepath.Join(dir, "corrupt.tar")
	require.NoError(t, os.WriteFile(corrupt, bz, 0600))
	require.Error(t, Restore(corrupt, filepath.Join(dir, "bad.db"), "identity", genesis, result.SHA256))
	// Even if a new outer checksum is supplied, the inner database hash must agree.
	h := sha256.Sum256(bz)
	require.Error(t, Restore(corrupt, filepath.Join(dir, "bad.db"), "identity", genesis, hex.EncodeToString(h[:])))
}

func TestLegacyBindingRequiresExplicitTrust(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "agent.db"), "identity")
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.Put("meta", "cursor", 3))
	require.Error(t, s.BindGenesis(strings.Repeat("ab", 32), false))
	require.NoError(t, s.BindGenesis(strings.Repeat("ab", 32), true))
}

func TestRetryAllowlistRejectsInvalidRequestsAndReplayedRequestsDoNotLoop(t *testing.T) {
	require.False(t, Retryable(Intent{Status: "failed", Attempts: []Attempt{{CheckCode: 18, CheckCodespace: "sdk"}}}))
	require.True(t, Retryable(Intent{Status: "failed", Attempts: []Attempt{{CheckCode: 20, CheckCodespace: "sdk"}}}))
	s, err := Open(filepath.Join(t.TempDir(), "agent.db"), "identity")
	require.NoError(t, err)
	defer s.Close()
	f := &fakeChain{results: map[string]chainclient.TxResult{}, checkCode: 11}
	m := newTestManager(s, f)
	enqueue(t, m)
	require.Error(t, m.Step(context.Background(), 1))
	require.NoError(t, m.Recover(context.Background(), "execute/auto-test", 2, "repair attempt", retryMessages, "repair-1"))
	require.Error(t, m.Step(context.Background(), 3))
	require.Equal(t, "failed", readIntent(t, s).Status)
	require.NoError(t, m.Recover(context.Background(), "execute/auto-test", 4, "repair attempt", func(Intent) ([]sdk.Msg, error) {
		t.Fatal("request replay must not retry another failure")
		return nil, nil
	}, "repair-1"))
	require.Equal(t, "failed", readIntent(t, s).Status)
	require.Len(t, readIntent(t, s).Recoveries, 1)
	require.Len(t, f.sent, 2)
}

func TestExplicitRecoveryCanRefreshAStaleLocalEvaluation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "agent.db"), "identity")
	require.NoError(t, err)
	defer s.Close()
	f := &fakeChain{results: map[string]chainclient.TxResult{}, checkCode: 11}
	m := newTestManager(s, f)
	enqueue(t, m)
	require.Error(t, m.Step(context.Background(), 1))
	m.Satisfied = func(context.Context, Intent, uint64) (bool, error) { return false, ErrReevaluationRequired }
	prepared := false
	require.NoError(t, m.Recover(context.Background(), "execute/auto-test", 2, "repair stale review", func(i Intent) ([]sdk.Msg, error) { prepared = true; return retryMessages(i) }))
	require.True(t, prepared)
	require.Equal(t, "queued", readIntent(t, s).Status)
}

func TestOversizedManifestIsNotPublished(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.db")
	s, err := Open(path, strings.Repeat("identity", 1000))
	require.NoError(t, err)
	require.NoError(t, s.BindGenesis(strings.Repeat("ab", 32), false))
	require.NoError(t, s.Close())
	archive := filepath.Join(dir, "oversized.tar")
	_, err = Backup(path, archive)
	require.ErrorContains(t, err, "manifest exceeds")
	_, err = os.Stat(archive)
	require.True(t, os.IsNotExist(err))
}
