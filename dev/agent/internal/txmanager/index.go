package txmanager

import (
	"encoding/json"
	"fmt"

	st "github.com/yanagihalab/G503/x/sanction/types"
	bolt "go.etcd.io/bbolt"
)

const journalIndexVersion = "1"

var indexVersionKey = []byte("active_index_version")

func activeIndex(bucket string) string {
	switch bucket {
	case "intents":
		return "active_intents"
	case "work":
		return "active_work"
	default:
		return ""
	}
}

func activeRow(bucket string, row []byte) (bool, error) {
	switch bucket {
	case "intents":
		var header struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(row, &header); err != nil {
			return false, fmt.Errorf("invalid intent header: %w", err)
		}
		switch header.Status {
		case "queued", "pending", "failed":
			// Failed intents still need semantic reconciliation and ownership fencing.
			return true, nil
		case "confirmed", "satisfied", "cancelled":
			return false, nil
		default:
			return false, fmt.Errorf("unknown intent status: %q", header.Status)
		}
	case "work":
		var header struct {
			Done   bool   `json:"done"`
			Result string `json:"result"`
		}
		if err := json.Unmarshal(row, &header); err != nil {
			return false, fmt.Errorf("invalid work header: %w", err)
		}
		return !header.Done || header.Result == st.CaseStatus_CASE_STATUS_EXPIRED.String(), nil
	default:
		return false, fmt.Errorf("bucket has no active index: %s", bucket)
	}
}

// Upgrade legacy journals once. Primary rows and derived indexes commit atomically.
func migrateIndexes(tx *bolt.Tx) error {
	meta := tx.Bucket([]byte("meta"))
	version := meta.Get(indexVersionKey)
	if version != nil && string(version) != journalIndexVersion {
		return fmt.Errorf("unsupported journal index version: %q", version)
	}
	if version != nil && tx.Bucket([]byte(activeIndex("intents"))) != nil && tx.Bucket([]byte(activeIndex("work"))) != nil {
		return nil
	}
	for _, bucket := range []string{"intents", "work"} {
		name := []byte(activeIndex(bucket))
		if tx.Bucket(name) != nil {
			if err := tx.DeleteBucket(name); err != nil {
				return err
			}
		}
		index, err := tx.CreateBucket(name)
		if err != nil {
			return err
		}
		if err := tx.Bucket([]byte(bucket)).ForEach(func(key, row []byte) error {
			active, err := activeRow(bucket, row)
			if err != nil {
				return fmt.Errorf("migrate %s/%s: %w", bucket, key, err)
			}
			if active {
				return index.Put(key, []byte{1})
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return meta.Put(indexVersionKey, []byte(journalIndexVersion))
}

// ListActive preserves primary-key ordering without loading completed history.
func (s *Store) ListActive(bucket string) ([]json.RawMessage, error) {
	name := activeIndex(bucket)
	if name == "" {
		return nil, fmt.Errorf("bucket has no active index: %s", bucket)
	}
	var rows []json.RawMessage
	err := s.db.View(func(tx *bolt.Tx) error {
		primary, index := tx.Bucket([]byte(bucket)), tx.Bucket([]byte(name))
		if primary == nil || index == nil {
			return fmt.Errorf("journal bucket or index is missing: %s", bucket)
		}
		return index.ForEach(func(key, _ []byte) error {
			row := primary.Get(key)
			if row == nil {
				return fmt.Errorf("journal index references missing %s/%s", bucket, key)
			}
			active, err := activeRow(bucket, row)
			if err != nil {
				return err
			}
			if !active {
				return fmt.Errorf("journal index references completed %s/%s", bucket, key)
			}
			rows = append(rows, append(json.RawMessage(nil), row...))
			return nil
		})
	})
	return rows, err
}

// Offline backup validation may scan history; running agents never need this scan.
func validateIndexes(tx *bolt.Tx) error {
	version := tx.Bucket([]byte("meta")).Get(indexVersionKey)
	if version == nil {
		return nil // Old backups are migrated when opened by the agent.
	}
	if string(version) != journalIndexVersion {
		return fmt.Errorf("unsupported journal index version: %q", version)
	}
	for _, bucket := range []string{"intents", "work"} {
		primary, index := tx.Bucket([]byte(bucket)), tx.Bucket([]byte(activeIndex(bucket)))
		if index == nil {
			return fmt.Errorf("journal index is missing: %s", bucket)
		}
		expected := 0
		if err := primary.ForEach(func(key, row []byte) error {
			active, err := activeRow(bucket, row)
			if err != nil {
				return err
			}
			if active {
				expected++
			}
			if (index.Get(key) != nil) != active {
				return fmt.Errorf("journal index membership mismatch: %s/%s", bucket, key)
			}
			return nil
		}); err != nil {
			return err
		}
		if index.Stats().KeyN != expected {
			return fmt.Errorf("journal index contains extra entries: %s", bucket)
		}
	}
	return nil
}
