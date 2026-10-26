package txmanager

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	st "github.com/yanagihalab/G503/x/sanction/types"
)

func TestFencedAttemptIsReconciledThenResignedAndLostOwnerCannotSend(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "agent.db"), "identity")
	require.NoError(t, err)
	defer s.Close()
	f := &fakeChain{results: map[string]chainclient.TxResult{}}
	m := newTestManager(s, f)
	m.Options.Memo = "g503-lease:1:old"
	enqueue(t, m)
	m.OnEvent = func(i Intent) {
		if len(i.Attempts) > 0 {
			panic("crash before broadcast")
		}
	}
	require.Panics(t, func() { _ = m.Step(context.Background(), 1) })
	old := readIntent(t, s).Attempts[0].Tx
	m.OnEvent = nil
	m.Options.Memo = "g503-lease:2:new"
	lost := errors.New("ownership lost")
	m.BeforeSend = func(context.Context) error { return lost }
	require.ErrorIs(t, m.Step(context.Background(), 2), lost)
	require.Empty(t, f.sent)
	m.BeforeSend = func(context.Context) error { return nil }
	require.NoError(t, m.Step(context.Background(), 2))
	require.Len(t, f.sent, 1)
	require.NotEqual(t, old, f.sent[0])
	i := readIntent(t, s)
	require.Len(t, i.Attempts, 2)
	require.True(t, i.Attempts[0].Resolved)
	require.Len(t, i.Recoveries, 1)
}

func TestDefinitivelyFencedFailureCanResumeOnlyUnderANewerSession(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "agent.db"), "identity")
	require.NoError(t, err)
	defer s.Close()
	f := &fakeChain{results: map[string]chainclient.TxResult{}, checkCode: st.ErrLeaseFenced.ABCICode()}
	m := newTestManager(s, f)
	m.Options.Memo = "g503-lease:1:old"
	enqueue(t, m)
	require.Error(t, m.Step(context.Background(), 1))
	i := readIntent(t, s)
	i.Attempts[0].CheckCodespace = "sanction"
	require.NoError(t, s.Put("intents", i.ID, i))
	require.NoError(t, m.Step(context.Background(), 2))
	require.Equal(t, "failed", readIntent(t, s).Status)
	m.Options.Memo = "g503-lease:2:new"
	f.checkCode = 0
	require.NoError(t, m.Step(context.Background(), 3))
	require.Equal(t, "queued", readIntent(t, s).Status)
	require.NoError(t, m.Step(context.Background(), 4))
	require.Len(t, f.sent, 2)
	require.Len(t, readIntent(t, s).Attempts, 2)
}

func TestRequeueDoesNotBroadcastAnotherIntentInTheSameStep(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "agent.db"), "identity")
	require.NoError(t, err)
	defer s.Close()
	f := &fakeChain{results: map[string]chainclient.TxResult{}, checkCode: st.ErrLeaseFenced.ABCICode()}
	m := newTestManager(s, f)
	m.Options.Memo = "g503-lease:1:old"
	enqueue(t, m)
	require.Error(t, m.Step(context.Background(), 1))
	i := readIntent(t, s)
	i.Attempts[0].CheckCodespace = st.ModuleName
	require.NoError(t, s.Put("intents", i.ID, i))
	require.NoError(t, m.Enqueue("execute/auto-z", "execute", "auto-z", &st.MsgExecuteSanction{Executor: "g50310xcqpzrky6eff2g52qdye53xkk9jxkvrnsxwnj", CaseId: "auto-z"}))
	m.Options.Memo = "g503-lease:2:new"
	f.checkCode, f.sent = 0, nil
	require.NoError(t, m.Step(context.Background(), 2))
	require.Empty(t, f.sent)
	require.Equal(t, "queued", readIntent(t, s).Status)
}
