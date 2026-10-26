package rpcguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	"github.com/yanagihalab/G503/dev/agent/internal/signer"
	bolt "go.etcd.io/bbolt"
)

type Options struct {
	Endpoints string
	StateDir  string
	RequestID string
	Namespace string
	Policy    chainclient.RPCSafety
}

func (o *Options) Flags(fs *flag.FlagSet) {
	fs.StringVar(&o.Endpoints, "rpc-endpoints", "", "comma-separated guarded RPC endpoints")
	fs.StringVar(&o.StateDir, "rpc-state-dir", "", "private persistent RPC checkpoint directory; defaults to G503_RPC_STATE_DIR or user state directory")
	fs.StringVar(&o.RequestID, "request-id", "", "stable idempotency ID; reuse only for the same operation")
	fs.StringVar(&o.Policy.GenesisHash, "rpc-genesis-hash", "", "optional trusted genesis fingerprint")
	fs.DurationVar(&o.Policy.MaxBlockAge, "rpc-max-block-age", 15*time.Second, "maximum committed block age")
	fs.DurationVar(&o.Policy.MaxFutureSkew, "rpc-max-future-skew", 3*time.Second, "maximum future block clock skew")
	fs.DurationVar(&o.Policy.ProbeTimeout, "rpc-probe-timeout", 3*time.Second, "per-endpoint safety probe timeout")
	fs.Uint64Var(&o.Policy.MaxHeightLag, "rpc-max-height-lag", 2, "maximum height lag")
	fs.IntVar(&o.Policy.MinAgreement, "rpc-min-agreement", 0, "matching endpoints; zero requires configured majority")
}

type Session struct {
	Client *chainclient.Client
	db     *bolt.DB
	sendMu sync.Mutex
}

func Open(chainID, fallback string, o Options) (*Session, error) {
	dir := o.StateDir
	if dir == "" {
		dir = os.Getenv("G503_RPC_STATE_DIR")
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".local", "state", "g503", "rpc")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("RPC state directory must be private and not a symlink")
	}
	sum := sha256.Sum256([]byte(chainID + "\x00" + o.Namespace))
	path := filepath.Join(dir, hex.EncodeToString(sum[:])+".db")
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return nil, fmt.Errorf("RPC journal must be a private regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 100 * time.Millisecond})
	if err != nil {
		return nil, fmt.Errorf("lock RPC journal: %w", err)
	}
	s := &Session{db: db}
	policy := o.Policy
	policy.ChainID = chainID
	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("rpc"))
		if err != nil {
			return err
		}
		if bz := b.Get([]byte("checkpoint")); bz != nil {
			return json.Unmarshal(bz, &policy.Checkpoint)
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	policy.Persist = func(cp chainclient.Checkpoint) error { return s.put("checkpoint", cp) }
	endpoints := []string{fallback}
	if o.Endpoints != "" {
		endpoints = strings.Split(o.Endpoints, ",")
	}
	s.Client, err = chainclient.NewResilient(endpoints, 10*time.Second, policy)
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Session) Close() error { return s.db.Close() }
func (s *Session) put(key string, value any) error {
	bz, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("rpc")).Put([]byte(key), bz) })
}

type attempt struct {
	Intent   string `json:"intent"`
	Tx       []byte `json:"tx"`
	Hash     string `json:"hash"`
	Sequence uint64 `json:"sequence"`
}

// Send preserves original signed bytes across lost acknowledgements. It never
// guesses that a consumed sequence means success or signs a replacement request.
func (s *Session) Send(ctx context.Context, id string, options chainclient.SignOptions, msgs ...sdk.Msg) (chainclient.BroadcastResult, error) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if id == "" {
		return s.Client.BroadcastSignedMsgs(ctx, options, msgs...)
	}
	var zero chainclient.BroadcastResult
	if len(id) > 200 {
		return zero, fmt.Errorf("request ID exceeds 200 bytes")
	}
	identity := options
	identity.PrivateKeyHex = ""
	identity.AccountNumber, identity.Sequence = 0, 0
	key, err := signer.ParsePrivateKey(options.PrivateKeyHex)
	if err != nil {
		return zero, err
	}
	unsigned, err := chainclient.EncodeUnsignedTx(msgs...)
	if err != nil {
		return zero, err
	}
	intent, err := json.Marshal(struct {
		Options  chainclient.SignOptions
		Signer   string
		Messages []byte
	}{identity, sdk.AccAddress(key.PubKey().Address()).String(), unsigned})
	if err != nil {
		return zero, err
	}
	intentHash := sha256.Sum256(intent)
	idHash := sha256.Sum256([]byte(id))
	recordKey := "request/" + hex.EncodeToString(idHash[:])
	var saved attempt
	found := false
	err = s.db.View(func(tx *bolt.Tx) error {
		if bz := tx.Bucket([]byte("rpc")).Get([]byte(recordKey)); bz != nil {
			found = true
			return json.Unmarshal(bz, &saved)
		}
		return nil
	})
	if err != nil {
		return zero, err
	}
	if found && (saved.Intent == "" || saved.Hash == "" || len(saved.Tx) == 0) {
		return zero, fmt.Errorf("incomplete saved signed request; refusing replacement")
	}
	status, err := s.Client.Status(ctx)
	if err != nil {
		return zero, err
	}
	if saved.Intent != "" {
		if saved.Intent != hex.EncodeToString(intentHash[:]) {
			return zero, fmt.Errorf("request ID is already bound to a different operation")
		}
		actual := sha256.Sum256(saved.Tx)
		if !strings.EqualFold(saved.Hash, hex.EncodeToString(actual[:])) {
			return zero, fmt.Errorf("corrupt saved signed transaction")
		}
		result, err := s.Client.LookupTx(ctx, saved.Hash)
		if err != nil {
			return zero, err
		}
		if result.Found {
			if result.Code != 0 {
				return zero, fmt.Errorf("saved request finalized with code %d", result.Code)
			}
			return chainclient.BroadcastResult{Hash: saved.Hash, Code: 0, Log: "reconciled committed request"}, nil
		}
		account, err := s.Client.AccountAt(ctx, sdk.AccAddress(key.PubKey().Address()).String(), status.Height)
		if err != nil {
			return zero, err
		}
		if account.Sequence != saved.Sequence {
			return zero, fmt.Errorf("saved request outcome unresolved: account sequence changed; refusing replacement")
		}
	} else {
		tx, err := chainclient.EncodeSignedTx(options, msgs...)
		if err != nil {
			return zero, err
		}
		hash := sha256.Sum256(tx)
		saved = attempt{Intent: hex.EncodeToString(intentHash[:]), Tx: tx, Hash: strings.ToUpper(hex.EncodeToString(hash[:])), Sequence: options.Sequence}
		if err := s.put(recordKey, saved); err != nil {
			return zero, err
		}
	}
	return s.Client.BroadcastTxSync(ctx, saved.Tx)
}
