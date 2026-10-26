package txmanager

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cometbft/cometbft/crypto/tmhash"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/yanagihalab/G503/app"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	st "github.com/yanagihalab/G503/x/sanction/types"
)

var ErrObsolete = errors.New("intent is no longer applicable")

type Attempt struct {
	Tx              []byte `json:"tx"`
	Hash            string `json:"hash"`
	Sequence        uint64 `json:"sequence"`
	Height          uint64 `json:"height"`
	Code            uint32 `json:"code"`
	Codespace       string `json:"codespace,omitempty"`
	CheckCode       uint32 `json:"check_code,omitempty"`
	CheckCodespace  string `json:"check_codespace,omitempty"`
	Resolved        bool   `json:"resolved"`
	Broadcasts      uint64 `json:"broadcasts"`
	NextBroadcastAt int64  `json:"next_broadcast_at,omitempty"`
}

type Intent struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	CaseID          string     `json:"case_id"`
	Messages        [][]byte   `json:"messages"`
	Status          string     `json:"status"`
	Attempts        []Attempt  `json:"attempts"`
	LastError       string     `json:"last_error,omitempty"`
	CompletedHeight uint64     `json:"completed_height,omitempty"`
	Recoveries      []Recovery `json:"recoveries,omitempty"`
}

type Recovery struct {
	RequestID        string   `json:"request_id,omitempty"`
	Height           uint64   `json:"height"`
	Reason           string   `json:"reason"`
	PreviousError    string   `json:"previous_error,omitempty"`
	PreviousMessages [][]byte `json:"previous_messages,omitempty"`
}

func (i Intent) Terminal() bool {
	return i.Status == "confirmed" || i.Status == "satisfied" || i.Status == "failed" || i.Status == "cancelled"
}

type Chain interface {
	AccountAt(context.Context, string, uint64) (chainclient.Account, error)
	LookupTx(context.Context, string) (chainclient.TxResult, error)
	BroadcastTxSync(context.Context, []byte) (chainclient.BroadcastResult, error)
}

type Manager struct {
	mu            sync.Mutex
	Store         *Store
	Chain         Chain
	Address       string
	Options       chainclient.SignOptions
	Satisfied     func(context.Context, Intent, uint64) (bool, error)
	OnEvent       func(Intent)
	RetryInterval time.Duration
	BeforeSend    func(context.Context) error
}

func (m *Manager) Enqueue(id, kind, caseID string, msgs ...sdk.Msg) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var existing Intent
	if found, err := m.Store.Get("intents", id, &existing); err != nil {
		return err
	} else if found {
		if existing.Kind != kind || existing.CaseID != caseID {
			return fmt.Errorf("intent ID belongs to a different operation")
		}
		return nil
	}
	encoded, err := marshalMessages(msgs)
	if err != nil {
		return err
	}
	i := Intent{ID: id, Kind: kind, CaseID: caseID, Status: "queued", Messages: encoded}
	return m.Store.Put("intents", id, i)
}

func (m *Manager) save(i Intent) error {
	if err := m.Store.Put("intents", i.ID, i); err != nil {
		return err
	}
	if m.OnEvent != nil {
		m.OnEvent(i)
	}
	return nil
}

// Step serializes this account: persist signed bytes before I/O and confirm before using the next sequence.
func (m *Manager) Step(ctx context.Context, height uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, err := m.Store.ListActive("intents")
	if err != nil {
		return err
	}
	for _, row := range rows {
		var i Intent
		if err = json.Unmarshal(row, &i); err != nil {
			return err
		}
		if i.Status == "failed" {
			if requeued, err := m.reconcileFailed(ctx, &i, height); err != nil || requeued {
				return err
			}
			continue
		}
		if i.Terminal() {
			continue
		}
		var last *Attempt
		if len(i.Attempts) > 0 {
			last = &i.Attempts[len(i.Attempts)-1]
		}
		if last != nil && !last.Resolved {
			result, e := m.Chain.LookupTx(ctx, last.Hash)
			if e != nil {
				return e
			}
			if result.Found {
				if result.Height > height {
					return nil
				}
				last.Height, last.Code, last.Resolved = result.Height, result.Code, true
				last.Codespace = result.Codespace
				if result.Code == 0 {
					i.Status = "confirmed"
					i.CompletedHeight = result.Height
					i.LastError = ""
					return m.save(i)
				}
				i.LastError = fmt.Sprintf("DeliverTx code=%d: %s", result.Code, result.Log)
				// A definite application failure consumes sequence; reconciliation decides whether another agent completed it.
			}
		}
		done, e := m.Satisfied(ctx, i, height)
		if errors.Is(e, ErrObsolete) {
			i.Status = "cancelled"
			return m.save(i)
		}
		if e != nil {
			return e
		}
		if done {
			i.Status = "satisfied"
			i.CompletedHeight = height
			i.LastError = ""
			return m.save(i)
		}
		account, e := m.Chain.AccountAt(ctx, m.Address, height)
		if e != nil {
			return e
		}
		if last != nil && m.fencedMemo(last.Tx) {
			if !last.Resolved || (last.Code == st.ErrLeaseFenced.ABCICode() && last.Codespace == "sanction") {
				// A finalized newer epoch fences the old signed attempt, including one still in a mempool.
				last.Resolved = true
				i.Status = "queued"
				i.Recoveries = append(i.Recoveries, Recovery{Height: height, Reason: "re-sign fenced attempt after ownership transfer"})
			}
		}
		if last != nil && !last.Resolved && account.Sequence < last.Sequence {
			return fmt.Errorf("account sequence moved backwards")
		}
		if last == nil || last.Resolved || account.Sequence > last.Sequence {
			if last != nil && last.Resolved && i.Status != "queued" {
				// Do not endlessly resubmit deterministic failures such as out-of-gas or expired voting.
				i.Status = "failed"
				return m.save(i)
			}
			msgs := make([]sdk.Msg, len(i.Messages))
			cdc := app.MakeEncodingConfig().Codec
			for n, bz := range i.Messages {
				if e = cdc.UnmarshalInterface(bz, &msgs[n]); e != nil {
					return e
				}
			}
			opts := m.Options
			opts.AccountNumber = account.Number
			opts.Sequence = account.Sequence
			bz, e := chainclient.EncodeSignedTx(opts, msgs...)
			if e != nil {
				return e
			}
			i.Attempts = append(i.Attempts, Attempt{Tx: bz, Hash: strings.ToUpper(hex.EncodeToString(tmhash.Sum(bz))), Sequence: account.Sequence})
			i.Status = "pending"
			last = &i.Attempts[len(i.Attempts)-1]
		}
		return m.broadcastAttempt(ctx, &i, last)
	}
	return nil
}

func (m *Manager) reconcileFailed(ctx context.Context, i *Intent, height uint64) (bool, error) {
	done, err := m.reconcile(ctx, i, height)
	if err != nil || done || len(i.Attempts) == 0 {
		return false, err
	}
	last := &i.Attempts[len(i.Attempts)-1]
	code := st.ErrLeaseFenced.ABCICode()
	fenced := (last.Codespace == st.ModuleName && last.Code == code) ||
		(last.CheckCodespace == st.ModuleName && last.CheckCode == code)
	if !fenced || !m.fencedMemo(last.Tx) {
		return false, nil
	}
	last.Resolved = true
	i.Status = "queued"
	i.Recoveries = append(i.Recoveries, Recovery{Height: height, Reason: "resume definitively fenced intent under new ownership"})
	return true, m.save(*i)
}

func (m *Manager) broadcastAttempt(ctx context.Context, i *Intent, last *Attempt) error {
	if time.Now().UnixNano() < last.NextBroadcastAt {
		return nil
	}
	if m.BeforeSend != nil {
		if err := m.BeforeSend(ctx); err != nil {
			return err
		}
	}
	retry := m.RetryInterval
	if retry == 0 {
		retry = 2 * time.Second
	}
	last.NextBroadcastAt = time.Now().Add(retry).UnixNano()
	last.Broadcasts++
	// Persist before network I/O so a lost acknowledgement cannot erase the attempt.
	if err := m.save(*i); err != nil {
		return err
	}
	result, sendErr := m.Chain.BroadcastTxSync(ctx, last.Tx)
	i.LastError = ""
	if sendErr != nil {
		i.LastError = sendErr.Error()
	}
	if sendErr != nil && result.Code != 0 && result.Code != sdkerrors.ErrWrongSequence.ABCICode() && result.Code != sdkerrors.ErrTxInMempoolCache.ABCICode() {
		i.Status = "failed"
		last.CheckCode, last.CheckCodespace = result.Code, result.Codespace
	}
	if err := m.save(*i); err != nil {
		return err
	}
	return sendErr
}

func (m *Manager) fencedMemo(bz []byte) bool {
	if m.Options.Memo == "" {
		return false
	}
	tx, err := app.MakeEncodingConfig().TxConfig.TxDecoder()(bz)
	if err != nil {
		return false
	}
	memo, ok := tx.(sdk.TxWithMemo)
	return ok && memo.GetMemo() != m.Options.Memo
}
