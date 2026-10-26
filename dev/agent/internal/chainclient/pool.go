package chainclient

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Checkpoint is an observed common block, not a cryptographically verified light-client header.
type Checkpoint struct {
	GenesisHash string `json:"genesis_hash"`
	Height      uint64 `json:"height"`
	BlockHash   string `json:"block_hash"`
}

type RPCSafety struct {
	ChainID       string
	GenesisHash   string
	MaxBlockAge   time.Duration
	MaxFutureSkew time.Duration
	MaxHeightLag  uint64
	MinAgreement  int
	ProbeTimeout  time.Duration
	Checkpoint    Checkpoint
	Persist       func(Checkpoint) error
	OnBroadcast   func(Checkpoint, string)
}

type rpcPool struct {
	mu         sync.Mutex
	clients    []*Client
	policy     RPCSafety
	checkpoint Checkpoint
	selected   *Client
	genesis    map[string]string
}

func NewResilient(endpoints []string, timeout time.Duration, policy RPCSafety) (*Client, error) {
	if len(endpoints) == 0 || policy.ChainID == "" || timeout <= 0 {
		return nil, fmt.Errorf("RPC endpoints, chain ID and positive timeout are required")
	}
	if policy.MaxBlockAge == 0 {
		policy.MaxBlockAge = 15 * time.Second
	}
	if policy.MaxFutureSkew == 0 {
		policy.MaxFutureSkew = 3 * time.Second
	}
	if policy.ProbeTimeout == 0 {
		policy.ProbeTimeout = 3 * time.Second
	}
	if policy.MaxBlockAge < 0 || policy.MaxFutureSkew < 0 || policy.ProbeTimeout <= 0 {
		return nil, fmt.Errorf("RPC safety durations must be positive")
	}
	if policy.Checkpoint.Height > 0 {
		if !validHash(policy.Checkpoint.BlockHash) || !validHash(policy.Checkpoint.GenesisHash) {
			return nil, fmt.Errorf("invalid saved RPC checkpoint")
		}
		if policy.GenesisHash != "" && !strings.EqualFold(policy.GenesisHash, policy.Checkpoint.GenesisHash) {
			return nil, fmt.Errorf("saved checkpoint genesis mismatch")
		}
		policy.GenesisHash = policy.Checkpoint.GenesisHash
	} else if policy.Checkpoint.BlockHash != "" || policy.Checkpoint.GenesisHash != "" {
		return nil, fmt.Errorf("incomplete saved RPC checkpoint")
	}
	if policy.GenesisHash != "" && !validHash(policy.GenesisHash) {
		return nil, fmt.Errorf("invalid trusted genesis hash")
	}
	pool := &rpcPool{policy: policy, checkpoint: policy.Checkpoint, genesis: map[string]string{}}
	seen := map[string]bool{}
	for _, endpoint := range endpoints {
		endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || seen[endpoint] {
			return nil, fmt.Errorf("RPC endpoints must be distinct HTTP(S) URLs without credentials/query/fragment")
		}
		seen[endpoint] = true
		pool.clients = append(pool.clients, New(endpoint, timeout))
	}
	if pool.policy.MinAgreement == 0 {
		pool.policy.MinAgreement = len(pool.clients)/2 + 1
	}
	if pool.policy.MinAgreement < 1 || pool.policy.MinAgreement > len(pool.clients) {
		return nil, fmt.Errorf("RPC minimum agreement exceeds endpoint count")
	}
	return &Client{pool: pool}, nil
}

func validHash(value string) bool {
	bz, err := hex.DecodeString(value)
	return err == nil && len(bz) == 32
}

type blockReference struct {
	Hash string
	Time time.Time
}

func (c *Client) blockReference(ctx context.Context, height uint64, chainID string) (blockReference, error) {
	raw, err := c.rpc(ctx, "block", map[string]string{"height": strconv.FormatUint(height, 10)})
	if err != nil {
		return blockReference{}, err
	}
	var result struct {
		ID struct {
			Hash string `json:"hash"`
		} `json:"block_id"`
		Block struct {
			Header struct {
				ChainID string    `json:"chain_id"`
				Height  string    `json:"height"`
				Time    time.Time `json:"time"`
			} `json:"header"`
		} `json:"block"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return blockReference{}, err
	}
	if result.Block.Header.ChainID != chainID || result.Block.Header.Height != strconv.FormatUint(height, 10) || !validHash(result.ID.Hash) || result.Block.Header.Time.IsZero() {
		return blockReference{}, fmt.Errorf("invalid checkpoint block response")
	}
	return blockReference{Hash: strings.ToUpper(result.ID.Hash), Time: result.Block.Header.Time}, nil
}

func (p *rpcPool) fresh(status Status, now time.Time) error {
	if status.ChainID != p.policy.ChainID || status.Height == 0 || status.Height < p.checkpoint.Height || !status.syncKnown || status.CatchingUp || !validHash(status.BlockHash) || status.BlockTime.IsZero() {
		return fmt.Errorf("wrong chain, catching up, missing metadata or height rollback")
	}
	if now.Sub(status.BlockTime) > p.policy.MaxBlockAge || status.BlockTime.Sub(now) > p.policy.MaxFutureSkew {
		return fmt.Errorf("stale or future block time")
	}
	return nil
}

type rpcCandidate struct {
	client  *Client
	status  Status
	genesis string
	block   blockReference
	err     error
}

// selectEndpoint is called under mu. Probe failures never lower the agreement requirement.
func (p *rpcPool) selectEndpoint(ctx context.Context) (json.RawMessage, error) {
	p.selected = nil
	candidates := make([]rpcCandidate, len(p.clients))
	var wg sync.WaitGroup
	for n, client := range p.clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			probe, cancel := context.WithTimeout(ctx, p.policy.ProbeTimeout)
			defer cancel()
			candidate := &candidates[n]
			candidate.client = client
			candidate.status, candidate.err = client.Status(probe)
			if candidate.err == nil {
				candidate.err = p.fresh(candidate.status, time.Now())
			}
			if candidate.err != nil {
				return
			}
			candidate.genesis = p.genesis[client.Endpoint]
			if candidate.genesis == "" {
				candidate.genesis, candidate.err = client.GenesisHash(probe, p.policy.ChainID)
			}
			if candidate.err == nil && p.policy.GenesisHash != "" && !strings.EqualFold(candidate.genesis, p.policy.GenesisHash) {
				candidate.err = fmt.Errorf("RPC genesis mismatch")
			}
			if candidate.err == nil && p.checkpoint.Height > 0 {
				var block blockReference
				block, candidate.err = client.blockReference(probe, p.checkpoint.Height, p.policy.ChainID)
				if candidate.err == nil && !strings.EqualFold(block.Hash, p.checkpoint.BlockHash) {
					candidate.err = fmt.Errorf("RPC checkpoint continuity mismatch")
				}
			}
		}()
	}
	wg.Wait()
	var highest uint64
	for _, candidate := range candidates {
		if candidate.err == nil && candidate.status.Height > highest {
			highest = candidate.status.Height
		}
	}
	common := highest
	for n := range candidates {
		c := &candidates[n]
		if c.err == nil && highest-c.status.Height > p.policy.MaxHeightLag {
			c.err = fmt.Errorf("RPC too far behind the freshest eligible endpoint")
		}
		if c.err == nil && c.status.Height < common {
			common = c.status.Height
		}
	}
	for n := range candidates {
		c := &candidates[n]
		if c.err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			probe, cancel := context.WithTimeout(ctx, p.policy.ProbeTimeout)
			defer cancel()
			c.block, c.err = c.client.blockReference(probe, common, p.policy.ChainID)
		}()
	}
	wg.Wait()
	groups := map[string][]*rpcCandidate{}
	for n := range candidates {
		c := &candidates[n]
		if c.err != nil {
			continue
		}
		if (common == c.status.Height && !strings.EqualFold(c.block.Hash, c.status.BlockHash)) || time.Since(c.block.Time) > p.policy.MaxBlockAge || time.Until(c.block.Time) > p.policy.MaxFutureSkew {
			c.err = fmt.Errorf("common block is stale or inconsistent with status")
			continue
		}
		key := c.genesis + "/" + c.block.Hash
		groups[key] = append(groups[key], c)
	}
	var agreed []*rpcCandidate
	for _, group := range groups {
		if len(group) < p.policy.MinAgreement {
			continue
		}
		if agreed != nil {
			return nil, fmt.Errorf("conflicting RPC groups meet minimum agreement; stop")
		}
		agreed = group
	}
	if len(agreed) == 0 {
		reasons := make([]string, 0, len(candidates))
		for n, candidate := range candidates {
			if candidate.err != nil {
				reasons = append(reasons, fmt.Sprintf("endpoint[%d]: %v", n, candidate.err))
			}
		}
		return nil, fmt.Errorf("no fresh continuous RPC group meets agreement %d: %s", p.policy.MinAgreement, strings.Join(reasons, "; "))
	}
	sort.SliceStable(agreed, func(i, j int) bool { return agreed[i].status.Height > agreed[j].status.Height })
	chosen := agreed[0]
	next := Checkpoint{GenesisHash: chosen.genesis, Height: common, BlockHash: chosen.block.Hash}
	if next.Height < p.checkpoint.Height {
		return nil, fmt.Errorf("RPC checkpoint moved backwards")
	}
	if next != p.checkpoint && p.policy.Persist != nil {
		if err := p.policy.Persist(next); err != nil {
			return nil, fmt.Errorf("persist RPC checkpoint: %w", err)
		}
	}
	p.checkpoint = next
	p.policy.GenesisHash = next.GenesisHash
	for _, candidate := range agreed {
		p.genesis[candidate.client.Endpoint] = candidate.genesis
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if time.Since(chosen.block.Time) > p.policy.MaxBlockAge || time.Until(chosen.block.Time) > p.policy.MaxFutureSkew {
		return nil, fmt.Errorf("common block expired while persisting checkpoint")
	}
	p.selected = chosen.client
	// Expose the common height so all subsequent pinned reads use the same observed history.
	return json.Marshal(map[string]any{"node_info": map[string]string{"network": p.policy.ChainID}, "sync_info": map[string]any{
		"latest_block_height": strconv.FormatUint(common, 10), "latest_block_hash": next.BlockHash,
		"latest_block_time": chosen.block.Time, "catching_up": false,
	}})
}

func (p *rpcPool) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if method == "status" || method == "broadcast_tx_sync" || p.selected == nil {
		status, err := p.selectEndpoint(ctx)
		if err != nil {
			return nil, err
		}
		if method == "status" {
			return status, nil
		}
	}
	// In particular, never try another endpoint after an ambiguous broadcast acknowledgement.
	if method == "broadcast_tx_sync" && p.policy.OnBroadcast != nil {
		p.policy.OnBroadcast(p.checkpoint, p.selected.Endpoint)
	}
	return p.selected.rpc(ctx, method, params)
}

func (c *Client) PendingTransactions(ctx context.Context, limit int) ([][]byte, error) {
	if limit <= 0 {
		limit = 100
	}
	raw, err := c.rpc(ctx, "unconfirmed_txs", map[string]string{"limit": strconv.Itoa(limit)})
	if err != nil {
		return nil, err
	}
	var response struct {
		Txs [][]byte `json:"txs"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	return response.Txs, nil
}

func (c *Client) RPCObservation() (Checkpoint, string) {
	if c.pool == nil {
		return Checkpoint{}, c.Endpoint
	}
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()
	endpoint := ""
	if c.pool.selected != nil {
		endpoint = c.pool.selected.Endpoint
	}
	return c.pool.checkpoint, endpoint
}
