package keeper

import (
	"context"
	"fmt"
	"math"
	"strings"

	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	"github.com/yanagihalab/G503/x/sanction/types"
)

const DefaultLeaseTTL uint64 = 24

func LeaseMemo(lease *types.AgentLease) string {
	return fmt.Sprintf("g503-lease:%d:%s", lease.Epoch, lease.HolderId)
}

func (k Keeper) GetAgentLease(ctx sdk.Context, address string) (*types.AgentLease, bool) {
	bz := k.Store(ctx).Get(append(append([]byte{}, types.AgentLeaseKeyPrefix...), []byte(address)...))
	if bz == nil {
		return nil, false
	}
	var lease = &types.AgentLease{}
	types.ModuleCdc.MustUnmarshal(bz, lease)
	return lease, true
}

func (k Keeper) SetAgentLease(ctx sdk.Context, lease *types.AgentLease) {
	k.Store(ctx).Set(append(append([]byte{}, types.AgentLeaseKeyPrefix...), []byte(lease.SignerAddress)...), types.ModuleCdc.MustMarshal(lease))
}

func (k Keeper) GetAllAgentLeases(ctx sdk.Context) []*types.AgentLease {
	it := storetypes.KVStorePrefixIterator(k.Store(ctx), types.AgentLeaseKeyPrefix)
	defer it.Close()
	var out []*types.AgentLease
	for ; it.Valid(); it.Next() {
		var v = &types.AgentLease{}
		types.ModuleCdc.MustUnmarshal(it.Value(), v)
		out = append(out, v)
	}
	return out
}

func (m msgServer) ClaimAgentLease(goCtx context.Context, msg *types.MsgClaimAgentLease) (*types.MsgClaimAgentLeaseResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	if _, err := sdk.AccAddressFromBech32(msg.SignerAddress); err != nil {
		return nil, err
	}
	if len(msg.HolderId) != 64 || strings.Trim(msg.HolderId, "0123456789abcdef") != "" || msg.TtlBlocks < 6 || msg.TtlBlocks > 100 {
		return nil, fmt.Errorf("invalid lease holder or TTL (6..100 blocks)")
	}
	if _, found := m.GetAgentBySigner(ctx, msg.SignerAddress); !found {
		return nil, types.ErrAgentNotFound
	}
	lease, found := m.GetAgentLease(ctx, msg.SignerAddress)
	if !found {
		lease = &types.AgentLease{SignerAddress: msg.SignerAddress}
	}
	if lease.Epoch != msg.ExpectedEpoch {
		return nil, fmt.Errorf("lease epoch changed")
	}
	h := uint64(ctx.BlockHeight())
	if msg.Renew {
		if lease.Epoch == 0 || lease.HolderId != msg.HolderId || lease.ExpiresHeight <= h {
			return nil, fmt.Errorf("lease lost; expired sessions cannot renew")
		}
	} else {
		if lease.ExpiresHeight > h {
			return nil, fmt.Errorf("signing key already has a live owner")
		}
		if lease.Epoch == math.MaxUint64 {
			return nil, fmt.Errorf("lease epoch exhausted")
		}
		lease.Epoch++
		lease.HolderId = msg.HolderId
	}
	lease.ExpiresHeight = h + msg.TtlBlocks
	m.SetAgentLease(ctx, lease)
	ctx.EventManager().EmitEvent(sdk.NewEvent("agent_lease_claimed", sdk.NewAttribute("signer", lease.SignerAddress), sdk.NewAttribute("holder", lease.HolderId)))
	return &types.MsgClaimAgentLeaseResponse{Lease: lease}, nil
}

func (q Keeper) AgentLease(goCtx context.Context, req *types.QueryAgentLeaseRequest) (*types.QueryAgentLeaseResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	if _, err := sdk.AccAddressFromBech32(req.SignerAddress); err != nil {
		return nil, err
	}
	lease, _ := q.GetAgentLease(sdk.UnwrapSDKContext(goCtx), req.SignerAddress)
	return &types.QueryAgentLeaseResponse{Lease: lease}, nil
}

// Validate again at DeliverTx: acceptance into an old owner's mempool is not authority to execute.
func (k Keeper) CheckAgentLeaseTx(ctx sdk.Context, tx sdk.Tx) error {
	claimSigner := ""
	for _, msg := range tx.GetMsgs() {
		if claim, ok := msg.(*types.MsgClaimAgentLease); ok {
			if len(tx.GetMsgs()) != 1 {
				return fmt.Errorf("lease claim must be a standalone transaction")
			}
			claimSigner = claim.SignerAddress
		}
	}
	verifiable, ok := tx.(authsigning.SigVerifiableTx)
	if !ok {
		return fmt.Errorf("transaction does not expose signatures")
	}
	signers, err := verifiable.GetSigners()
	if err != nil {
		return err
	}
	for _, signer := range signers {
		// Use SDK-derived required signers, including cached-key transactions.
		address := sdk.AccAddress(signer).String()
		if address == claimSigner {
			continue
		}
		lease, found := k.GetAgentLease(ctx, address)
		if !found {
			continue
		} // Existing non-daemon signers retain their pre-lease behavior.
		memo, ok := tx.(sdk.TxWithMemo)
		if !ok || memo.GetMemo() != LeaseMemo(lease) || lease.ExpiresHeight <= uint64(ctx.BlockHeight()) {
			return types.ErrLeaseFenced
		}
	}
	return nil
}
