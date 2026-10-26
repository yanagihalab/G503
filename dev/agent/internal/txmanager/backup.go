package txmanager

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cometbft/cometbft/crypto/tmhash"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/yanagihalab/G503/app"
	st "github.com/yanagihalab/G503/x/sanction/types"
	bolt "go.etcd.io/bbolt"
)

const maxBackupSize int64 = 128 << 20

type BackupManifest struct {
	Version      int    `json:"version"`
	Identity     string `json:"identity"`
	GenesisHash  string `json:"genesis_hash"`
	DatabaseHash string `json:"database_sha256"`
	Size         int64  `json:"database_size"`
	CreatedAt    string `json:"created_at"`
}

type BackupResult struct {
	Path     string         `json:"path"`
	SHA256   string         `json:"sha256"`
	Manifest BackupManifest `json:"manifest"`
}

func regularPrivate(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("journal/backup must be an owner-only regular file")
	}
	if info.Size() > maxBackupSize {
		return fmt.Errorf("journal/backup exceeds 128 MiB limit")
	}
	return nil
}

func existingDB(path string) (db *bolt.DB, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("invalid journal: %v", p)
		}
	}()
	if err = regularPrivate(path); err != nil {
		return nil, err
	}
	// Exclusive lock: backup/restore validation is offline, never a copy of a live mmap.
	return bolt.Open(path, 0600, &bolt.Options{Timeout: 100 * time.Millisecond})
}

func manifestFor(db *bolt.DB) (m BackupManifest, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("invalid journal contents: %v", p)
		}
	}()
	err = db.View(func(tx *bolt.Tx) error {
		var checkErr error
		for e := range tx.Check() {
			if checkErr == nil {
				checkErr = e
			}
		}
		if checkErr != nil {
			return checkErr
		}
		meta := tx.Bucket([]byte("meta"))
		if meta == nil || tx.Bucket([]byte("work")) == nil || tx.Bucket([]byte("intents")) == nil {
			return fmt.Errorf("invalid journal buckets")
		}
		m.Identity = string(meta.Get([]byte("identity")))
		if err := json.Unmarshal(meta.Get([]byte("genesis_hash")), &m.GenesisHash); err != nil {
			return fmt.Errorf("journal must be bound to genesis before backup")
		}
		if !validDigest(m.GenesisHash) || m.Identity == "" {
			return fmt.Errorf("invalid journal identity")
		}
		if bz := meta.Get([]byte("cursor")); bz != nil {
			var cursor uint64
			if err := json.Unmarshal(bz, &cursor); err != nil {
				return fmt.Errorf("invalid cursor: %w", err)
			}
		}
		for _, name := range []string{"work", "intents"} {
			if err := tx.Bucket([]byte(name)).ForEach(func(k, v []byte) error {
				if !json.Valid(v) {
					return fmt.Errorf("invalid %s row", name)
				}
				if name == "intents" {
					var i Intent
					if err := json.Unmarshal(v, &i); err != nil {
						return err
					}
					if i.ID != string(k) || i.ID != i.Kind+"/"+i.CaseID || (i.Status != "queued" && i.Status != "pending" && !i.Terminal()) {
						return fmt.Errorf("invalid intent identity/status")
					}
					if err := validateMessages(i); err != nil {
						return err
					}
					for _, a := range i.Attempts {
						if !strings.EqualFold(a.Hash, hex.EncodeToString(tmhash.Sum(a.Tx))) {
							return fmt.Errorf("signed attempt hash mismatch")
						}
					}
				} else {
					var work struct {
						Hash   string `json:"hash"`
						CaseID string `json:"case_id"`
					}
					if err := json.Unmarshal(v, &work); err != nil {
						return err
					}
					if !validDigest(work.Hash) || string(k) != "auto-"+strings.ToUpper(work.Hash) || (work.CaseID != string(k) && !strings.HasPrefix(work.CaseID, string(k)+"-r")) {
						return fmt.Errorf("invalid work identity")
					}
				}
				return nil
			}); err != nil {
				return err
			}
		}
		return validateIndexes(tx)
	})
	return m, err
}

func validateMessages(i Intent) error {
	cdc := app.MakeEncodingConfig().Codec
	if len(i.Messages) == 0 {
		return fmt.Errorf("intent has no messages")
	}
	control := 0
	for _, bz := range i.Messages {
		var msg sdk.Msg
		if err := cdc.UnmarshalInterface(bz, &msg); err != nil {
			return err
		}
		switch m := msg.(type) {
		case *st.MsgSubmitRiskReport:
			if i.Kind != "open" || m.Report == nil {
				return fmt.Errorf("invalid report intent")
			}
		case *st.MsgOpenSanctionCase:
			if i.Kind != "open" || m.CaseId != i.CaseID {
				return fmt.Errorf("invalid open intent")
			}
			control++
		case *st.MsgSubmitSanctionVote:
			if i.Kind != "vote" || m.Vote == nil || m.Vote.CaseId != i.CaseID {
				return fmt.Errorf("invalid vote intent")
			}
			control++
		case *st.MsgExecuteSanction:
			if i.Kind != "execute" || m.CaseId != i.CaseID {
				return fmt.Errorf("invalid execute intent")
			}
			control++
		default:
			return fmt.Errorf("unexpected recovery message")
		}
	}
	if control != 1 {
		return fmt.Errorf("intent must contain one control operation")
	}
	return nil
}

func validDigest(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == sha256.Size
}

func syncDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Publish via hard link in the same filesystem: never overwrite an existing file.
func publish(temp, target string) error {
	if err := os.Link(temp, target); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

func Backup(journal, archive string) (result BackupResult, err error) {
	db, err := existingDB(journal)
	if err != nil {
		return result, err
	}
	defer db.Close()
	m, err := manifestFor(db)
	if err != nil {
		return result, err
	}
	if err = os.MkdirAll(filepath.Dir(archive), 0700); err != nil {
		return result, err
	}
	snapshot, err := os.CreateTemp(filepath.Dir(archive), ".journal-snapshot-*")
	if err != nil {
		return result, err
	}
	defer os.Remove(snapshot.Name())
	defer snapshot.Close()
	h := sha256.New()
	err = db.View(func(tx *bolt.Tx) error { var e error; m.Size, e = tx.WriteTo(io.MultiWriter(snapshot, h)); return e })
	if err != nil {
		return result, err
	}
	if m.Size > maxBackupSize-4096 {
		return result, fmt.Errorf("snapshot exceeds backup size limit")
	}
	m.Version, m.DatabaseHash, m.CreatedAt = 1, hex.EncodeToString(h.Sum(nil)), time.Now().UTC().Format(time.RFC3339)
	metadata, err := json.Marshal(m)
	if err != nil {
		return result, err
	}
	if len(metadata) > 4096 {
		return result, fmt.Errorf("backup manifest exceeds 4096 byte limit")
	}
	out, err := os.CreateTemp(filepath.Dir(archive), ".journal-backup-*")
	if err != nil {
		return result, err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	archiveHash := sha256.New()
	tw := tar.NewWriter(io.MultiWriter(out, archiveHash))
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(metadata))}); err != nil {
		return result, err
	}
	if _, err = tw.Write(metadata); err != nil {
		return result, err
	}
	if err = tw.WriteHeader(&tar.Header{Name: "agent.db", Mode: 0600, Size: m.Size}); err != nil {
		return result, err
	}
	if _, err = snapshot.Seek(0, io.SeekStart); err != nil {
		return result, err
	}
	if _, err = io.Copy(tw, snapshot); err != nil {
		return result, err
	}
	if err = tw.Close(); err != nil {
		return result, err
	}
	if err = out.Sync(); err != nil {
		return result, err
	}
	if err = publish(out.Name(), archive); err != nil {
		return result, err
	}
	return BackupResult{Path: archive, SHA256: hex.EncodeToString(archiveHash.Sum(nil)), Manifest: m}, nil
}

// The trusted outer checksum is required; the embedded digest alone is not authentication.
func Restore(archive, journal, identity, genesis, checksum string) (err error) {
	if !validDigest(checksum) || !validDigest(genesis) {
		return fmt.Errorf("trusted backup SHA-256 and genesis hash are required")
	}
	if err = regularPrivate(archive); err != nil {
		return err
	}
	in, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer in.Close()
	h := sha256.New()
	if _, err = io.Copy(h, in); err != nil {
		return err
	}
	if !strings.EqualFold(checksum, hex.EncodeToString(h.Sum(nil))) {
		return fmt.Errorf("backup checksum mismatch")
	}
	if _, err = in.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err = os.Lstat(journal); !os.IsNotExist(err) {
		return fmt.Errorf("restore destination already exists or cannot be checked")
	}
	if err = os.MkdirAll(filepath.Dir(journal), 0700); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(journal), ".journal-restore-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	tr := tar.NewReader(in)
	header, err := tr.Next()
	if err != nil {
		return err
	}
	if header.Name != "manifest.json" || header.Typeflag != tar.TypeReg || header.Size > 4096 {
		return fmt.Errorf("invalid backup manifest entry")
	}
	bz, err := io.ReadAll(io.LimitReader(tr, 4097))
	if err != nil {
		return err
	}
	var m BackupManifest
	if err = json.Unmarshal(bz, &m); err != nil {
		return err
	}
	if m.Version != 1 || m.Identity != identity || m.GenesisHash != genesis || m.Size <= 0 || m.Size > maxBackupSize {
		return fmt.Errorf("backup identity/genesis/version mismatch")
	}
	header, err = tr.Next()
	if err != nil {
		return err
	}
	if header.Name != "agent.db" || header.Typeflag != tar.TypeReg || header.Size != m.Size {
		return fmt.Errorf("invalid backup database entry")
	}
	h.Reset()
	n, err := io.Copy(io.MultiWriter(out, h), tr)
	if err != nil {
		return err
	}
	if n != m.Size || hex.EncodeToString(h.Sum(nil)) != m.DatabaseHash {
		return fmt.Errorf("database checksum mismatch")
	}
	if _, err = tr.Next(); err != io.EOF {
		return fmt.Errorf("unexpected backup entry")
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	db, err := existingDB(out.Name())
	if err != nil {
		return err
	}
	actual, err := manifestFor(db)
	closeErr := db.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if actual.Identity != identity || actual.GenesisHash != genesis {
		return fmt.Errorf("database identity mismatch")
	}
	return publish(out.Name(), journal)
}

func (s *Store) BindGenesis(hash string, allowLegacy bool) error {
	if !validDigest(hash) {
		return fmt.Errorf("invalid genesis hash")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("meta"))
		var existing string
		if bz := b.Get([]byte("genesis_hash")); bz != nil {
			if err := json.Unmarshal(bz, &existing); err != nil {
				return err
			}
			if existing != hash {
				return fmt.Errorf("journal genesis mismatch")
			}
			return nil
		}
		if !allowLegacy && (b.Get([]byte("cursor")) != nil || tx.Bucket([]byte("work")).Stats().KeyN != 0 || tx.Bucket([]byte("intents")).Stats().KeyN != 0) {
			return fmt.Errorf("legacy journal requires explicit trusted genesis binding")
		}
		bz, _ := json.Marshal(hash)
		return b.Put([]byte("genesis_hash"), bz)
	})
}
