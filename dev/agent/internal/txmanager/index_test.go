package txmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	bolt "go.etcd.io/bbolt"
)

type indexedWork struct {
	Hash   string `json:"hash"`
	Done   bool   `json:"done"`
	Result string `json:"result"`
}

func activeIDs(t testing.TB, s *Store, bucket string) []string {
	t.Helper()
	rows, err := s.ListActive(bucket)
	require.NoError(t, err)
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		var header struct {
			ID   string `json:"id"`
			Hash string `json:"hash"`
		}
		require.NoError(t, json.Unmarshal(row, &header))
		if bucket == "work" {
			ids = append(ids, header.Hash)
		} else {
			ids = append(ids, header.ID)
		}
	}
	return ids
}

func TestActiveIndexesTrackTransitionsAndPreserveRecovery(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "agent.db"), "test")
	require.NoError(t, err)
	defer s.Close()
	for _, status := range []string{"queued", "pending", "failed", "confirmed", "satisfied", "cancelled", "failed", "queued"} {
		i := Intent{ID: "execute/a", Kind: "execute", CaseID: "a", Status: status}
		require.NoError(t, s.Put("intents", i.ID, i))
		ids := activeIDs(t, s, "intents")
		if status == "queued" || status == "pending" || status == "failed" {
			require.Equal(t, []string{i.ID}, ids)
		} else {
			require.Empty(t, ids)
		}
	}
	require.Error(t, s.Put("intents", "execute/a", Intent{Status: "unknown"}))
	var persisted Intent
	_, err = s.Get("intents", "execute/a", &persisted)
	require.NoError(t, err)
	require.Equal(t, "queued", persisted.Status)
	for _, work := range []indexedWork{
		{Hash: "a"},
		{Hash: "b", Done: true, Result: "executed"},
		{Hash: "c", Done: true, Result: "CASE_STATUS_EXPIRED"},
		{Hash: "d", Done: true, Result: "no_action"},
	} {
		require.NoError(t, s.Put("work", work.Hash, work))
	}
	require.Equal(t, []string{"a", "c"}, activeIDs(t, s, "work"))
	require.NoError(t, s.Put("work", "b", indexedWork{Hash: "b"}))
	require.Equal(t, []string{"a", "b", "c"}, activeIDs(t, s, "work"))
	require.NoError(t, s.Put("work", "c", indexedWork{Hash: "c", Done: true, Result: "executed"}))
	require.Equal(t, []string{"a", "b"}, activeIDs(t, s, "work"))
	all, err := s.List("work")
	require.NoError(t, err)
	require.Len(t, all, 4, "completion must retain the full audit history")
	_, err = s.ListActive("meta")
	require.Error(t, err)
}

func editOffline(t *testing.T, path string, edit func(*bolt.Tx) error) {
	t.Helper()
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	require.NoError(t, err)
	require.NoError(t, db.Update(edit))
	require.NoError(t, db.Close())
}

func removeIndexVersion(tx *bolt.Tx) error {
	for _, bucket := range []string{"intents", "work"} {
		if err := tx.DeleteBucket([]byte(activeIndex(bucket))); err != nil {
			return err
		}
	}
	return tx.Bucket([]byte("meta")).Delete(indexVersionKey)
}

func TestLegacyJournalMigrationRebuildsDerivedIndexesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	s, err := Open(path, "test")
	require.NoError(t, err)
	for _, status := range []string{"pending", "confirmed", "failed"} {
		require.NoError(t, s.Put("intents", status, Intent{ID: status, Status: status}))
	}
	require.NoError(t, s.Put("work", "expired", indexedWork{Hash: "expired", Done: true, Result: "CASE_STATUS_EXPIRED"}))
	require.NoError(t, s.Close())
	editOffline(t, path, func(tx *bolt.Tx) error {
		if err := removeIndexVersion(tx); err != nil {
			return err
		}
		partial, err := tx.CreateBucket([]byte(activeIndex("intents")))
		if err != nil {
			return err
		}
		return partial.Put([]byte("stale-entry"), []byte{1})
	})
	for range 2 {
		s, err = Open(path, "test")
		require.NoError(t, err)
		require.Equal(t, []string{"failed", "pending"}, activeIDs(t, s, "intents"))
		require.Equal(t, []string{"expired"}, activeIDs(t, s, "work"))
		require.NoError(t, s.db.View(func(tx *bolt.Tx) error { return validateIndexes(tx) }))
		require.NoError(t, s.Close())
	}
}

func TestFailedMigrationRollsBackWithoutPublishingPartialIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	s, err := Open(path, "test")
	require.NoError(t, err)
	require.NoError(t, s.Close())
	editOffline(t, path, func(tx *bolt.Tx) error {
		if err := removeIndexVersion(tx); err != nil {
			return err
		}
		return tx.Bucket([]byte("intents")).Put([]byte("bad"), []byte(`{"status":"unknown"}`))
	})
	_, err = Open(path, "test")
	require.ErrorContains(t, err, "unknown intent status")
	editOffline(t, path, func(tx *bolt.Tx) error {
		require.Nil(t, tx.Bucket([]byte(activeIndex("intents"))))
		require.Nil(t, tx.Bucket([]byte("meta")).Get(indexVersionKey))
		return tx.Bucket([]byte("intents")).Delete([]byte("bad"))
	})
	s, err = Open(path, "test")
	require.NoError(t, err)
	require.NoError(t, s.Close())
}

func TestMissingIndexRollsBackWritesAndRebuildsOnRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	s, err := Open(path, "test")
	require.NoError(t, err)
	i := Intent{ID: "execute/a", Status: "queued"}
	require.NoError(t, s.Put("intents", i.ID, i))
	require.NoError(t, s.db.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket([]byte(activeIndex("intents"))) }))
	i.Status = "confirmed"
	require.Error(t, s.Put("intents", i.ID, i))
	var original Intent
	_, err = s.Get("intents", i.ID, &original)
	require.NoError(t, err)
	require.Equal(t, "queued", original.Status)
	_, err = s.ListActive("intents")
	require.Error(t, err)
	require.NoError(t, s.Close())
	s, err = Open(path, "test")
	require.NoError(t, err)
	defer s.Close()
	require.Equal(t, []string{i.ID}, activeIDs(t, s, "intents"))
}

func TestIndexCorruptionFailsClosedAndCannotBeBackedUp(t *testing.T) {
	for _, corruption := range []string{"missing-member", "extra-member", "future-version"} {
		t.Run(corruption, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.db")
			s, err := Open(path, "identity")
			require.NoError(t, err)
			require.NoError(t, s.BindGenesis(strings.Repeat("ab", 32), false))
			m := newTestManager(s, &fakeChain{})
			enqueue(t, m)
			require.NoError(t, s.db.Update(func(tx *bolt.Tx) error {
				index := tx.Bucket([]byte(activeIndex("intents")))
				switch corruption {
				case "missing-member":
					return index.Delete([]byte("execute/auto-test"))
				case "extra-member":
					return index.Put([]byte("missing"), []byte{1})
				default:
					return tx.Bucket([]byte("meta")).Put(indexVersionKey, []byte("99"))
				}
			}))
			require.Error(t, s.db.View(validateIndexes))
			if corruption == "extra-member" {
				_, err = s.ListActive("intents")
				require.ErrorContains(t, err, "missing")
			}
			require.NoError(t, s.Close())
			_, err = Backup(path, filepath.Join(t.TempDir(), "invalid.tar"))
			require.Error(t, err)
			if corruption == "future-version" {
				_, err = Open(path, "identity")
				require.ErrorContains(t, err, "unsupported journal index version")
			}
		})
	}
}

func TestLegacyBackupRestoreMigratesAndReconcilesWithoutSending(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")
	s, err := Open(path, "identity")
	require.NoError(t, err)
	genesis := strings.Repeat("ab", 32)
	require.NoError(t, s.BindGenesis(genesis, false))
	f := &fakeChain{results: map[string]chainclient.TxResult{}, checkCode: 11}
	m := newTestManager(s, f)
	enqueue(t, m)
	require.Error(t, m.Step(context.Background(), 1))
	require.NoError(t, s.Close())
	editOffline(t, path, removeIndexVersion)
	archive := filepath.Join(dir, "old.tar")
	backup, err := Backup(path, archive)
	require.NoError(t, err)
	restored := filepath.Join(dir, "new.db")
	require.NoError(t, Restore(archive, restored, "identity", genesis, backup.SHA256))
	s, err = Open(restored, "identity")
	require.NoError(t, err)
	defer s.Close()
	require.Equal(t, []string{"execute/auto-test"}, activeIDs(t, s, "intents"))
	m = newTestManager(s, f)
	m.Satisfied = func(context.Context, Intent, uint64) (bool, error) { return true, nil }
	require.NoError(t, m.Step(context.Background(), 2))
	require.Empty(t, activeIDs(t, s, "intents"))
	require.Len(t, f.sent, 1)
}

func TestConcurrentJournalUpdatesKeepIndexesConsistent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "agent.db"), "test")
	require.NoError(t, err)
	defer s.Close()
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for n := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprint(n)
			for _, status := range []string{"queued", "pending", "confirmed"} {
				if err := s.Put("intents", id, Intent{ID: id, Status: status}); err != nil {
					errors <- err
					return
				}
				if _, err := s.ListActive("intents"); err != nil {
					errors <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.Empty(t, activeIDs(t, s, "intents"))
	require.NoError(t, s.db.View(validateIndexes))
}

func BenchmarkJournalActiveIndex(b *testing.B) {
	for _, history := range []int{100, 10000} {
		b.Run(fmt.Sprint(history), func(b *testing.B) {
			s, err := Open(filepath.Join(b.TempDir(), "agent.db"), "benchmark")
			require.NoError(b, err)
			defer s.Close()
			require.NoError(b, s.db.Update(func(tx *bolt.Tx) error {
				for n := range history + 5 {
					i := Intent{ID: fmt.Sprintf("execute/%08d", n), Status: "confirmed", Messages: [][]byte{make([]byte, 1024)}}
					if n >= history {
						i.Status = "pending"
						if err := tx.Bucket([]byte(activeIndex("intents"))).Put([]byte(i.ID), []byte{1}); err != nil {
							return err
						}
					}
					row, err := json.Marshal(i)
					if err != nil {
						return err
					}
					if err := tx.Bucket([]byte("intents")).Put([]byte(i.ID), row); err != nil {
						return err
					}
				}
				return nil
			}))
			require.Len(b, activeIDs(b, s, "intents"), 5)
			for _, indexed := range []bool{false, true} {
				b.Run(fmt.Sprintf("indexed=%t", indexed), func(b *testing.B) {
					b.ReportAllocs()
					for range b.N {
						var err error
						if indexed {
							_, err = s.ListActive("intents")
						} else {
							_, err = s.List("intents")
						}
						if err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}
