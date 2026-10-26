// Package ownership fences a cooperative agent process using finalized chain state.
package ownership

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	"github.com/yanagihalab/G503/x/sanction/keeper"
	st "github.com/yanagihalab/G503/x/sanction/types"
	"google.golang.org/protobuf/proto"
)

var ErrLost = errors.New("signing ownership lost; this process must stop")

type Session struct {
	RPC     *chainclient.Client
	Address string
	Options chainclient.SignOptions
	TTL     uint64
	Lease   *st.AgentLease
	holder  string
}

func Read(ctx context.Context, rpc *chainclient.Client, address string, height uint64) (*st.AgentLease, error) {
	bz, err := proto.Marshal(&st.QueryAgentLeaseRequest{SignerAddress: address})
	if err != nil {
		return nil, err
	}
	request := bz
	deadline := time.Now().Add(3 * time.Second)
	for {
		bz, err = rpc.Query(ctx, "/sanction.v1.Query/AgentLease", request, height)
		var queryErr *chainclient.QueryError
		if err == nil {
			break
		}
		if !errors.As(err, &queryErr) || queryErr.Code != 26 || !strings.Contains(queryErr.Log, "future") || time.Now().After(deadline) {
			return nil, err
		}
		if err = pause(ctx); err != nil {
			return nil, err
		}
	}
	var response = &st.QueryAgentLeaseResponse{}
	if err = proto.Unmarshal(bz, response); err != nil {
		return nil, err
	}
	return response.Lease, nil
}

func (s *Session) Acquire(ctx context.Context, wait time.Duration) error {
	if s.TTL == 0 {
		s.TTL = keeper.DefaultLeaseTTL
	}
	if s.TTL < 6 || s.TTL > 100 {
		return fmt.Errorf("lease TTL must be 6..100 blocks")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	s.holder = hex.EncodeToString(random[:]) // Never load process ownership from a copied journal.
	deadline := time.Now().Add(wait)
	for {
		status, err := s.RPC.Status(ctx)
		if err != nil {
			return err
		}
		if status.ChainID != s.Options.ChainID {
			return fmt.Errorf("ownership RPC chain ID mismatch")
		}
		lease, err := Read(ctx, s.RPC, s.Address, status.Height)
		if err != nil {
			return err
		}
		if lease == nil || lease.ExpiresHeight <= status.Height {
			if lease == nil {
				lease = &st.AgentLease{}
			}
			return s.claim(ctx, lease.Epoch, false)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("signing key already owned until height %d", lease.ExpiresHeight)
		}
		if err = pause(ctx); err != nil {
			return err
		}
	}
}

func pause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(200 * time.Millisecond):
		return nil
	}
}

func (s *Session) claim(ctx context.Context, epoch uint64, renew bool) error {
	status, err := s.RPC.Status(ctx)
	if err != nil {
		return err
	}
	if _, err = Read(ctx, s.RPC, s.Address, status.Height); err != nil {
		return err
	}
	account, err := s.RPC.AccountAt(ctx, s.Address, status.Height)
	if err != nil {
		return err
	}
	opts := s.Options
	opts.AccountNumber, opts.Sequence, opts.GasLimit, opts.Memo = account.Number, account.Sequence, 250000, ""
	msg := &st.MsgClaimAgentLease{SignerAddress: s.Address, HolderId: s.holder, ExpectedEpoch: epoch, TtlBlocks: s.TTL, Renew: renew}
	bz, err := chainclient.EncodeSignedTx(opts, msg)
	if err != nil {
		return err
	}
	// One submission only. Unknown results are reconciled against finalized lease state.
	_, broadcastErr := s.RPC.BroadcastTxSync(ctx, bz)
	deadline := time.Now().Add(30 * time.Second)
	for {
		status, err = s.RPC.Status(ctx)
		if err != nil {
			return err
		}
		lease, e := Read(ctx, s.RPC, s.Address, status.Height)
		if e != nil {
			return e
		}
		expected := epoch
		if !renew {
			expected++
		}
		if lease != nil && lease.Epoch == expected && lease.HolderId == s.holder && lease.ExpiresHeight > status.Height && (!renew || s.Lease == nil || lease.ExpiresHeight > s.Lease.ExpiresHeight) {
			s.Lease = lease
			return nil
		}
		if lease != nil && (lease.Epoch > expected || (lease.Epoch == expected && lease.HolderId != s.holder)) {
			return ErrLost
		}
		if broadcastErr != nil {
			return fmt.Errorf("lease acquisition not confirmed: %w", broadcastErr)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("lease acquisition result unknown; stop and reconcile on next startup")
		}
		if err = pause(ctx); err != nil {
			return err
		}
	}
}

func (s *Session) Validate(ctx context.Context) error {
	_, err := s.validatedStatus(ctx)
	return err
}

func (s *Session) validatedStatus(ctx context.Context) (chainclient.Status, error) {
	status, err := s.RPC.Status(ctx)
	if err != nil {
		return status, err
	}
	if status.ChainID != s.Options.ChainID {
		return status, fmt.Errorf("ownership RPC chain ID mismatch")
	}
	lease, err := Read(ctx, s.RPC, s.Address, status.Height)
	if err != nil {
		return status, err
	}
	if s.Lease == nil || lease == nil || lease.Epoch != s.Lease.Epoch || lease.HolderId != s.holder || lease.ExpiresHeight <= status.Height {
		return status, ErrLost
	}
	return status, nil
}

func (s *Session) Ensure(ctx context.Context) error {
	status, err := s.validatedStatus(ctx)
	if err != nil {
		return err
	}
	if s.Lease.ExpiresHeight <= status.Height+s.TTL/2 {
		return s.claim(ctx, s.Lease.Epoch, true)
	}
	return nil
}

func (s *Session) Memo() string { return keeper.LeaseMemo(s.Lease) }
