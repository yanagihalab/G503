package txmanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/yanagihalab/G503/app"
	st "github.com/yanagihalab/G503/x/sanction/types"
)

var ErrReevaluationRequired = errors.New("fresh independent evaluation required before execution")

// Reconcile first, even for failed intents restored from an old backup. Never send here.
func (m *Manager) reconcile(ctx context.Context, i *Intent, height uint64) (bool, error) {
	for n := range i.Attempts {
		a := &i.Attempts[n]
		r, err := m.Chain.LookupTx(ctx, a.Hash)
		if err != nil {
			return false, err
		}
		if r.Found {
			if r.Height > height {
				return false, fmt.Errorf("tx result is ahead of committed snapshot")
			}
			a.Resolved, a.Height, a.Code, a.Codespace = true, r.Height, r.Code, r.Codespace
			if r.Code == 0 {
				i.Status, i.CompletedHeight, i.LastError = "confirmed", r.Height, ""
				return true, m.save(*i)
			}
		}
	}
	done, err := m.Satisfied(ctx, *i, height)
	if errors.Is(err, ErrObsolete) {
		i.Status = "cancelled"
		return true, m.save(*i)
	}
	if err != nil {
		return false, err
	}
	if done {
		i.Status, i.CompletedHeight, i.LastError = "satisfied", height, ""
		return true, m.save(*i)
	}
	return false, nil
}

func Retryable(i Intent) bool {
	if i.Status != "failed" || len(i.Attempts) == 0 {
		return false
	}
	a := i.Attempts[len(i.Attempts)-1]
	code, space := a.Code, a.Codespace
	if a.CheckCode != 0 {
		code, space = a.CheckCode, a.CheckCodespace
	}
	if space == st.ModuleName {
		// Expired reports require independent re-evaluation before retry.
		return code == st.ErrRiskReportExpired.ABCICode()
	}
	if space != "" && space != "sdk" {
		return false
	}
	switch code {
	case sdkerrors.ErrInsufficientFunds.ABCICode(), sdkerrors.ErrOutOfGas.ABCICode(),
		sdkerrors.ErrInsufficientFee.ABCICode(), sdkerrors.ErrMempoolIsFull.ABCICode(),
		sdkerrors.ErrWrongSequence.ABCICode():
		return true
	default:
		return false
	}
}

func marshalMessages(msgs []sdk.Msg) ([][]byte, error) {
	if len(msgs) == 0 {
		return nil, fmt.Errorf("intent needs messages")
	}
	cdc := app.MakeEncodingConfig().Codec
	out := make([][]byte, len(msgs))
	for n, msg := range msgs {
		bz, err := cdc.MarshalInterface(msg)
		if err != nil {
			return nil, err
		}
		out[n] = bz
	}
	return out, nil
}

// Recover retries only an explicit failed intent. Unknown outcomes stay blocked.
// prepare runs after chain reconciliation and must revalidate the operation/evidence.
func (m *Manager) Recover(ctx context.Context, id string, height uint64, reason string, prepare func(Intent) ([]sdk.Msg, error), requestIDs ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(reason) == "" || len(reason) > 1024 {
		return fmt.Errorf("recovery reason must contain 1-1024 bytes")
	}
	digest := sha256.Sum256([]byte(reason))
	requestID := hex.EncodeToString(digest[:])
	if len(requestIDs) > 0 && requestIDs[0] != "" {
		requestID = requestIDs[0]
	}
	if len(requestID) > 128 {
		return fmt.Errorf("recovery request ID exceeds 128 bytes")
	}
	var i Intent
	found, err := m.Store.Get("intents", id, &i)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("intent not found")
	}
	for _, r := range i.Recoveries {
		if r.RequestID == requestID {
			if r.Reason != reason {
				return fmt.Errorf("recovery request ID reused with a different reason")
			}
			return nil
		}
	}
	if i.Terminal() && i.Status != "failed" {
		return nil
	}
	if i.Status != "failed" {
		return fmt.Errorf("only failed intents can be retried (status=%s)", i.Status)
	}
	if done, err := m.reconcile(ctx, &i, height); done || (err != nil && !errors.Is(err, ErrReevaluationRequired)) {
		return err
	}
	if !Retryable(i) {
		return fmt.Errorf("failure is not allowlisted for retry: %s", i.LastError)
	}
	account, err := m.Chain.AccountAt(ctx, m.Address, height)
	if err != nil {
		return err
	}
	for n := range i.Attempts {
		a := &i.Attempts[n]
		if account.Sequence < a.Sequence {
			return fmt.Errorf("account sequence moved backwards")
		}
		if !a.Resolved && a.CheckCode == 0 && account.Sequence <= a.Sequence {
			return fmt.Errorf("prior tx outcome is still uncertain")
		}
		a.Resolved = true
	}
	msgs, err := prepare(i)
	if errors.Is(err, ErrObsolete) {
		i.Status = "cancelled"
		return m.save(i)
	}
	if err != nil {
		return err
	}
	encoded, err := marshalMessages(msgs)
	if err != nil {
		return err
	}
	i.Recoveries = append(i.Recoveries, Recovery{RequestID: requestID, Height: height, Reason: reason, PreviousError: i.LastError, PreviousMessages: i.Messages})
	i.Messages, i.Status, i.LastError, i.CompletedHeight = encoded, "queued", "", 0
	return m.save(i)
}

// An unbroadcast queued payload can be refreshed without losing its audit trail.
func (m *Manager) RefreshQueued(id string, height uint64, msgs ...sdk.Msg) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var i Intent
	found, err := m.Store.Get("intents", id, &i)
	if err != nil || !found {
		return err
	}
	if i.Status != "queued" {
		return nil
	}
	for _, a := range i.Attempts {
		if !a.Resolved {
			return fmt.Errorf("cannot refresh an unresolved signed attempt")
		}
	}
	encoded, err := marshalMessages(msgs)
	if err != nil {
		return err
	}
	old, _ := json.Marshal(i.Messages)
	next, _ := json.Marshal(encoded)
	if bytes.Equal(old, next) {
		return nil
	}
	i.Recoveries = append(i.Recoveries, Recovery{Height: height, Reason: "refresh unbroadcast evidence", PreviousMessages: i.Messages})
	i.Messages = encoded
	return m.save(i)
}
