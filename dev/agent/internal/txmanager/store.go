package txmanager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

type Store struct{ db *bolt.DB }

func Open(path, identity string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return nil, fmt.Errorf("journal must be an owner-only regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 100 * time.Millisecond})
	if err != nil {
		return nil, fmt.Errorf("open journal (another agent may own it): %w", err)
	}
	s := &Store{db: db}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{"meta", "work", "intents"} {
			if _, e := tx.CreateBucketIfNotExists([]byte(name)); e != nil {
				return e
			}
		}
		b := tx.Bucket([]byte("meta"))
		if existing := b.Get([]byte("identity")); existing != nil && string(existing) != identity {
			return fmt.Errorf("journal chain/agent/signer identity mismatch")
		}
		if err := b.Put([]byte("identity"), []byte(identity)); err != nil {
			return err
		}
		return migrateIndexes(tx)
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Get(bucket, key string, out any) (bool, error) {
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		bz := tx.Bucket([]byte(bucket)).Get([]byte(key))
		if bz == nil {
			return nil
		}
		found = true
		return json.Unmarshal(bz, out)
	})
	return found, err
}

func (s *Store) Put(bucket, key string, value any) error {
	bz, err := json.Marshal(value)
	if err != nil {
		return err
	}
	index := activeIndex(bucket)
	active := false
	if index != "" {
		active, err = activeRow(bucket, bz)
		if err != nil {
			return err
		}
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		primary := tx.Bucket([]byte(bucket))
		if primary == nil {
			return fmt.Errorf("unknown journal bucket: %s", bucket)
		}
		if err := primary.Put([]byte(key), bz); err != nil {
			return err
		}
		if index == "" {
			return nil
		}
		secondary := tx.Bucket([]byte(index))
		if secondary == nil {
			return fmt.Errorf("journal index is missing: %s", index)
		}
		if active {
			return secondary.Put([]byte(key), []byte{1})
		}
		return secondary.Delete([]byte(key))
	})
}

func (s *Store) List(bucket string) ([]json.RawMessage, error) {
	var out []json.RawMessage
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(bucket)).ForEach(func(_, v []byte) error { out = append(out, json.RawMessage(append([]byte(nil), v...))); return nil })
	})
	return out, err
}

func Snapshot(path string) (map[string][]json.RawMessage, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{ReadOnly: true, Timeout: 100 * time.Millisecond})
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := map[string][]json.RawMessage{}
	err = db.View(func(tx *bolt.Tx) error {
		for _, name := range []string{"work", "intents"} {
			b := tx.Bucket([]byte(name))
			if b == nil {
				return fmt.Errorf("invalid journal")
			}
			if e := b.ForEach(func(_, v []byte) error {
				out[name] = append(out[name], json.RawMessage(append([]byte(nil), v...)))
				return nil
			}); e != nil {
				return e
			}
		}
		return nil
	})
	return out, err
}
