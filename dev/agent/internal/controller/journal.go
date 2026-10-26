package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

type Journal struct {
	db     *bolt.DB
	Owner  string
	stepMu sync.Mutex
}
type record struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Open fences concurrent controllers through an OS-backed lock, including after
// SIGKILL. Identity includes the immutable inventory, revision and scenario version.
func Open(path string, identity any, resume bool) (*Journal, error) {
	info, statErr := os.Lstat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	}
	if statErr == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return nil, fmt.Errorf("controller journal must be a private regular file")
	}
	if resume && os.IsNotExist(statErr) {
		return nil, fmt.Errorf("controller journal is missing")
	}
	if !resume && statErr == nil {
		return nil, fmt.Errorf("controller journal exists; explicit resume is required")
	}
	bz, err := json.Marshal(identity)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(bz)
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 100 * time.Millisecond})
	if err != nil {
		return nil, fmt.Errorf("controller is already running or journal unavailable: %w", err)
	}
	j := &Journal{db: db}
	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("controller"))
		if err != nil {
			return err
		}
		if resume {
			if string(b.Get([]byte("identity"))) != hex.EncodeToString(hash[:]) {
				return fmt.Errorf("controller inventory/revision/scenario mismatch")
			}
			j.Owner = string(b.Get([]byte("owner")))
			if len(j.Owner) != 64 {
				return fmt.Errorf("invalid controller owner")
			}
			if string(b.Get([]byte("finished"))) == "cleaned" {
				return fmt.Errorf("cleaned experiment cannot be resumed")
			}
			return nil
		}
		if b.Get([]byte("identity")) != nil {
			return fmt.Errorf("controller journal was concurrently initialized; explicit resume is required")
		}
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		j.Owner = hex.EncodeToString(nonce[:])
		if err := b.Put([]byte("identity"), []byte(hex.EncodeToString(hash[:]))); err != nil {
			return err
		}
		return b.Put([]byte("owner"), []byte(j.Owner))
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return j, nil
}

func (j *Journal) Close() error { return j.db.Close() }
func (j *Journal) Get(key string, target any) (bool, error) {
	found := false
	err := j.db.View(func(tx *bolt.Tx) error {
		bz := tx.Bucket([]byte("controller")).Get([]byte("data/" + key))
		if bz == nil {
			return nil
		}
		found = true
		return json.Unmarshal(bz, target)
	})
	return found, err
}
func (j *Journal) Put(key string, value any) error {
	bz, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return j.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("controller")).Put([]byte("data/"+key), bz) })
}

// Step re-enters an interrupted callback with the same operation ID. Callbacks
// must reconcile side effects (not blindly replay them); completed results are immutable.
func (j *Journal) Step(ctx context.Context, name string, result any, run func(context.Context, string) (any, error)) error {
	j.stepMu.Lock()
	defer j.stepMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	var saved record
	found, err := j.Get("step/"+name, &saved)
	if err != nil {
		return err
	}
	if found && saved.Status != "done" && saved.Status != "pending" && saved.Status != "interrupted" {
		return fmt.Errorf("invalid saved controller step status")
	}
	if saved.Status == "done" {
		if result != nil {
			return json.Unmarshal(saved.Result, result)
		}
		return nil
	}
	if err := j.Put("step/"+name, record{Status: "pending"}); err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(j.Owner + "/" + name))
	value, err := run(ctx, hex.EncodeToString(hash[:]))
	if err != nil {
		if saveErr := j.Put("step/"+name, record{Status: "interrupted", Error: err.Error()}); saveErr != nil {
			return fmt.Errorf("%w; persist interruption: %v", err, saveErr)
		}
		return err
	}
	bz, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := j.Put("step/"+name, record{Status: "done", Result: bz}); err != nil {
		return err
	}
	if result != nil {
		return json.Unmarshal(bz, result)
	}
	return nil
}

func (j *Journal) Finish(status string) error {
	if status != "passed" && status != "cleaned" {
		return fmt.Errorf("invalid controller terminal status")
	}
	return j.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("controller")).Put([]byte("finished"), []byte(status))
	})
}
