package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/yanagihalab/G503/app"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	"github.com/yanagihalab/G503/dev/agent/internal/ownership"
	"github.com/yanagihalab/G503/dev/agent/internal/signer"
	"github.com/yanagihalab/G503/dev/agent/internal/txmanager"
	"github.com/yanagihalab/G503/dev/agent/internal/watcher"
	"github.com/yanagihalab/G503/x/sanction/keeper"
	st "github.com/yanagihalab/G503/x/sanction/types"
	"google.golang.org/protobuf/proto"
)

type daemonWork struct {
	PreviousCaseID string                    `json:"previous_case_id,omitempty"`
	CaseID         string                    `json:"case_id"`
	Hash           string                    `json:"hash"`
	Transfers      []watcher.DecodedTransfer `json:"transfers"`
	SeenHeight     uint64                    `json:"seen_height"`
	ApprovedHeight uint64                    `json:"approved_height,omitempty"`
	Review         *output                   `json:"review,omitempty"`
	Done           bool                      `json:"done"`
	Result         string                    `json:"result,omitempty"`
}

type daemon struct {
	opts        processOptions
	rpc         *chainclient.Client
	store       *txmanager.Store
	manager     *txmanager.Manager
	signer      signer.Signer
	encoder     *json.Encoder
	limit       int
	owner       *ownership.Session
	rpcEndpoint string
}

func autoCaseID(hash string) string { return "auto-" + strings.ToUpper(hash) }

func runDaemon(ctx context.Context, opts processOptions, stateDir string, startHeight uint64, interval time.Duration, limit int, writer io.Writer) error {
	if stateDir == "" || opts.agentID == "" || opts.privKeyHex == "" {
		return fmt.Errorf("daemon requires state-dir, agent-id and signing key")
	}
	if opts.expiryHeight <= opts.height {
		return fmt.Errorf("invalid report TTL")
	}
	if _, err := parseTargetType(opts.targetType); err != nil {
		return err
	}
	s, err := signer.New(opts.agentID, opts.privKeyHex)
	if err != nil {
		return err
	}
	store, err := txmanager.Open(filepath.Join(stateDir, "agent.db"), journalIdentity(opts, s))
	if err != nil {
		return err
	}
	defer store.Close()
	policy := opts.rpcSafety
	encoder := json.NewEncoder(writer)
	policy.OnBroadcast = func(cp chainclient.Checkpoint, endpoint string) {
		_ = encoder.Encode(map[string]any{"event": "rpc_broadcast", "endpoint": endpoint, "checkpoint_height": cp.Height})
	}
	policy.ChainID = opts.chainID
	var savedGenesis string
	if _, err = store.Get("meta", "genesis_hash", &savedGenesis); err != nil {
		return err
	}
	policy.GenesisHash = savedGenesis
	if opts.genesisBinding != "" {
		if savedGenesis != "" && savedGenesis != opts.genesisBinding {
			return fmt.Errorf("trusted genesis hash mismatch")
		}
		policy.GenesisHash = opts.genesisBinding
	}
	if _, err = store.Get("meta", "rpc_checkpoint", &policy.Checkpoint); err != nil {
		return err
	}
	policy.Persist = func(cp chainclient.Checkpoint) error { return store.Put("meta", "rpc_checkpoint", cp) }
	endpoints := opts.rpcEndpoints
	if len(endpoints) == 0 {
		endpoints = []string{opts.rpcURL}
	}
	rpc, err := chainclient.NewResilient(endpoints, 10*time.Second, policy)
	if err != nil {
		return err
	}
	status, err := rpc.Status(ctx)
	if err != nil {
		return err
	}
	checkpoint, endpoint := rpc.RPCObservation()
	genesis := checkpoint.GenesisHash
	if err = store.BindGenesis(genesis, opts.genesisBinding != ""); err != nil {
		return err
	}
	d := &daemon{opts: opts, rpc: rpc, store: store, signer: s, encoder: encoder, limit: limit, rpcEndpoint: endpoint}
	d.event("rpc_selected", map[string]any{"endpoint": endpoint, "checkpoint_height": checkpoint.Height, "checkpoint_hash": checkpoint.BlockHash})
	fee, err := sdk.ParseCoinsNormalized(opts.fee)
	if err != nil {
		return err
	}
	d.manager = &txmanager.Manager{Store: store, Chain: rpc, Address: s.Address, Options: chainclient.SignOptions{ChainID: opts.chainID, PrivateKeyHex: opts.privKeyHex, GasLimit: opts.gasLimit, FeeAmount: fee}, Satisfied: d.satisfied, OnEvent: func(i txmanager.Intent) {
		hash := ""
		if len(i.Attempts) > 0 {
			hash = i.Attempts[len(i.Attempts)-1].Hash
		}
		d.event("tx_intent", map[string]any{"id": i.ID, "kind": i.Kind, "status": i.Status, "hash": hash, "height": i.CompletedHeight, "attempts": len(i.Attempts), "error": i.LastError})
	}}
	d.owner = &ownership.Session{RPC: rpc, Address: s.Address, Options: d.manager.Options, TTL: opts.leaseTTL}
	if err = d.owner.Acquire(ctx, opts.leaseWait); err != nil {
		return err
	}
	d.manager.Options.Memo = d.owner.Memo()
	d.manager.BeforeSend = d.owner.Validate
	d.event("ownership_acquired", map[string]any{"holder": d.owner.Lease.HolderId, "epoch": d.owner.Lease.Epoch, "expires_height": d.owner.Lease.ExpiresHeight})
	status, err = rpc.Status(ctx)
	if err != nil {
		return err
	}
	if opts.retryIntent != "" {
		if err = d.validateRegistry(ctx, status.Height); err != nil {
			return err
		}
		if err = d.recoverFailed(ctx, opts.retryIntent, status.Height, opts.retryReason); err != nil {
			return err
		}
	}
	var cursor uint64
	if found, e := store.Get("meta", "cursor", &cursor); e != nil {
		return e
	} else if !found {
		cursor = status.Height
		if startHeight > 0 {
			if startHeight > status.Height+1 {
				return fmt.Errorf("start-height is ahead of chain")
			}
			cursor = startHeight - 1
		}
		if err = store.Put("meta", "cursor", cursor); err != nil {
			return err
		}
	}
	if interval <= 0 {
		interval = time.Second
	}
	d.event("ready", map[string]any{"agent_id": opts.agentID, "cursor": cursor})
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = d.tick(ctx); err != nil {
			if errors.Is(err, ownership.ErrLost) {
				d.event("ownership_lost", map[string]any{"error": err.Error()})
				return err
			}
			d.event("retry", map[string]any{"error": err.Error()})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (d *daemon) event(name string, fields map[string]any) {
	fields["event"] = name
	_ = d.encoder.Encode(fields)
}

func (d *daemon) query(ctx context.Context, height uint64, method string, req, resp proto.Message) error {
	bz, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	bz, err = d.rpc.Query(ctx, "/sanction.v1.Query/"+method, bz, height)
	if err != nil {
		return err
	}
	return proto.Unmarshal(bz, resp)
}

func (d *daemon) getCase(ctx context.Context, id string, height uint64) (*st.SanctionCase, error) {
	if !strings.HasPrefix(id, "auto-") {
		return nil, fmt.Errorf("invalid automatic case ID")
	}
	hash, _, err := parseAutoCaseID(id)
	if err != nil {
		return nil, err
	}
	response := &st.QuerySanctionCasesByTxResponse{}
	if err := d.query(ctx, height, "SanctionCasesByTx", &st.QuerySanctionCasesByTxRequest{TxHash: hash}, response); err != nil {
		return nil, err
	}
	for _, c := range response.SanctionCases {
		if c.CaseId == id {
			return c, nil
		}
	}
	return nil, nil
}

func (d *daemon) satisfied(ctx context.Context, i txmanager.Intent, height uint64) (bool, error) {
	c, err := d.getCase(ctx, i.CaseID, height)
	if err != nil {
		return false, err
	}
	if i.Kind == "open" {
		if c == nil && d.store != nil {
			hash, _, e := parseAutoCaseID(i.CaseID)
			if e != nil {
				return false, e
			}
			var w daemonWork
			found, e := d.store.Get("work", autoCaseID(hash), &w)
			if e != nil {
				return false, e
			}
			if found && w.Done && w.Result == "no_action" {
				return false, txmanager.ErrObsolete
			}
		}
		return c != nil, nil
	}
	if c == nil {
		return false, nil
	}
	if i.Kind == "execute" && c.ExecutedHeight > 0 {
		record := &st.QueryExecutionRecordResponse{}
		if err = d.query(ctx, height, "ExecutionRecord", &st.QueryExecutionRecordRequest{CaseId: i.CaseID}, record); err != nil {
			return false, err
		}
		expectedResult := "ok"
		if c.RequestedAction == st.SanctionAction_SANCTION_ACTION_WATCH {
			expectedResult = "watch_only"
		}
		return record.Record != nil && record.Record.CaseId == i.CaseID && record.Record.ResultCode == expectedResult, nil
	}
	if i.Kind == "vote" {
		votes := &st.QuerySanctionVotesResponse{}
		if err = d.query(ctx, height, "SanctionVotes", &st.QuerySanctionVotesRequest{CaseId: i.CaseID}, votes); err != nil {
			return false, err
		}
		for _, v := range votes.Votes {
			if v.AgentId == d.opts.agentID {
				return true, nil
			}
		}
	}
	if c.Status == st.CaseStatus_CASE_STATUS_REJECTED || c.Status == st.CaseStatus_CASE_STATUS_REVOKED || c.Status == st.CaseStatus_CASE_STATUS_EXPIRED || c.Status == st.CaseStatus_CASE_STATUS_EXECUTED {
		return false, txmanager.ErrObsolete
	}
	if i.Kind == "vote" && c.Status == st.CaseStatus_CASE_STATUS_APPROVED && c.RequestedAction != st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER {
		return false, txmanager.ErrObsolete
	}
	if i.Kind == "execute" && d.store != nil {
		hash, _, e := parseAutoCaseID(i.CaseID)
		if e != nil {
			return false, e
		}
		var w daemonWork
		found, e := d.store.Get("work", autoCaseID(hash), &w)
		if e != nil {
			return false, e
		}
		if found && w.CaseID != i.CaseID {
			return false, txmanager.ErrObsolete
		}
		if !found || w.Review == nil || w.Review.RiskReport == nil || w.Review.RiskReport.ExpiryHeight <= height+1 {
			return false, txmanager.ErrReevaluationRequired
		}
		action, e := parseSanctionAction(w.Review.Action)
		if e != nil {
			return false, e
		}
		if action != c.RequestedAction {
			return false, txmanager.ErrObsolete
		}
	}
	return false, nil
}

func (d *daemon) observe(tx watcher.PendingTx, height uint64) error {
	transfers, err := watcher.DecodeTransfers(tx)
	if err != nil {
		return err
	}
	if len(transfers) == 0 {
		return nil
	}
	hash := strings.ToUpper(tx.Hash)
	id := autoCaseID(hash)
	var existing daemonWork
	if found, e := d.store.Get("work", id, &existing); e != nil {
		return e
	} else if found {
		return nil
	}
	for i := range transfers {
		transfers[i].TxHash = hash
	}
	work := daemonWork{CaseID: id, Hash: hash, Transfers: transfers, SeenHeight: height}
	if err = d.store.Put("work", id, work); err != nil {
		return err
	}
	d.event("detected", map[string]any{"case_id": id, "height": height})
	return nil
}

func (d *daemon) scan(ctx context.Context, height uint64) error {
	var cursor uint64
	if _, err := d.store.Get("meta", "cursor", &cursor); err != nil {
		return err
	}
	if cursor > height {
		return fmt.Errorf("chain tip is behind durable cursor")
	}
	end := height
	if end-cursor > 25 {
		end = cursor + 25
	}
	for h := cursor + 1; h <= end; h++ {
		txs, codes, err := d.rpc.BlockTxs(ctx, h)
		if err != nil {
			return err
		}
		for n, bz := range txs {
			if codes[n] != 0 {
				continue
			}
			hash := strings.ToUpper(hex.EncodeToString(keeper.TxHash(bz)))
			if err = d.observe(watcher.PendingTx{Bytes: bz, Hash: hash}, h); err != nil {
				return err
			}
			tx, err := app.MakeEncodingConfig().TxConfig.TxDecoder()(bz)
			if err != nil {
				return err
			}
			for _, msg := range tx.GetMsgs() {
				open, ok := msg.(*st.MsgOpenSanctionCase)
				if !ok {
					continue
				}
				caseHash, _, e := parseAutoCaseID(open.CaseId)
				if e != nil || !strings.EqualFold(caseHash, hex.EncodeToString(open.TxHash)) {
					continue
				}
				var existing daemonWork
				root := autoCaseID(caseHash)
				if found, e := d.store.Get("work", root, &existing); e != nil {
					return e
				} else if found {
					continue
				}
				if err = d.store.Put("work", root, daemonWork{CaseID: open.CaseId, PreviousCaseID: open.PreviousCaseId, Hash: strings.ToUpper(hex.EncodeToString(open.TxHash)), SeenHeight: h}); err != nil {
					return err
				}
			}
		}
		// Each observed item is durable before advancing the checkpoint; replay is idempotent.
		if err = d.store.Put("meta", "cursor", h); err != nil {
			return err
		}
	}
	return nil
}

func (d *daemon) tick(ctx context.Context) error {
	if d.owner != nil {
		if err := d.owner.Ensure(ctx); err != nil {
			return err
		}
	}
	status, err := d.rpc.Status(ctx)
	if err != nil {
		return err
	}
	if status.ChainID != d.opts.chainID {
		return fmt.Errorf("RPC chain ID mismatch")
	}
	if status.Height == 0 {
		return nil
	}
	h := status.Height
	checkpoint, endpoint := d.rpc.RPCObservation()
	if endpoint != d.rpcEndpoint {
		d.rpcEndpoint = endpoint
		d.event("rpc_selected", map[string]any{"endpoint": endpoint, "checkpoint_height": checkpoint.Height, "checkpoint_hash": checkpoint.BlockHash})
	}
	if h > ^uint64(0)-(d.opts.expiryHeight-d.opts.height) {
		return fmt.Errorf("report expiry height overflows")
	}
	if err = d.scan(ctx, h); err != nil {
		return err
	}
	pending, err := d.rpc.PendingTransactions(ctx, d.limit)
	if err != nil {
		return err
	}
	for _, tx := range pending {
		if err = d.observe(watcher.PendingTx{Bytes: tx, Hash: hex.EncodeToString(keeper.TxHash(tx))}, h); err != nil {
			d.event("decode_error", map[string]any{"error": err.Error()})
		}
	}
	agents := &st.QueryAgentsResponse{}
	if err = d.query(ctx, h, "Agents", &st.QueryAgentsRequest{}, agents); err != nil {
		return err
	}
	active := []*st.AgentInfo{}
	for _, a := range agents.Agents {
		if a.Active && a.VotingPower > 0 {
			active = append(active, a)
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].AgentId < active[j].AgentId })
	rank := -1
	for i, a := range active {
		if a.AgentId == d.opts.agentID && a.SignerAddress == d.signer.Address && bytes.Equal(a.PublicKey, d.signer.PublicKey()) {
			rank = i
		}
	}
	if rank < 0 {
		// Snapshot members may finish their old votes after leaving the live validator set.
		for _, a := range agents.Agents {
			if a.AgentId == d.opts.agentID && a.SignerAddress == d.signer.Address && bytes.Equal(a.PublicKey, d.signer.PublicKey()) {
				rank = len(active)
				break
			}
		}
		if rank < 0 {
			return fmt.Errorf("registered signer/key does not match")
		}
	}
	params := &st.QueryParamsResponse{}
	if err = d.query(ctx, h, "Params", &st.QueryParamsRequest{}, params); err != nil {
		return err
	}
	if params.Params == nil {
		return fmt.Errorf("missing sanction params")
	}
	rows, err := d.store.ListActive("work")
	if err != nil {
		return err
	}
	for _, row := range rows {
		var work daemonWork
		if err = json.Unmarshal(row, &work); err != nil {
			return err
		}
		if work.Done && work.Result == st.CaseStatus_CASE_STATUS_EXPIRED.String() {
			work.Done = false
		}
		if work.Done {
			continue
		}
		if err = d.progress(ctx, &work, h, rank, active, params.Params); err != nil {
			d.event("work_retry", map[string]any{"case_id": work.CaseID, "error": err.Error()})
			continue
		}
		if err = d.store.Put("work", autoCaseID(work.Hash), work); err != nil {
			return err
		}
	}
	return d.manager.Step(ctx, h)
}

func (d *daemon) progress(ctx context.Context, w *daemonWork, height uint64, rank int, active []*st.AgentInfo, params *st.Params) error {
	c, err := d.recoveryCase(ctx, w, height)
	if err != nil {
		return err
	}
	if c != nil && c.ExecutedHeight > 0 {
		done, e := d.satisfied(ctx, txmanager.Intent{Kind: "execute", CaseID: w.CaseID}, height)
		if e != nil {
			return e
		}
		if !done {
			return fmt.Errorf("execution record missing")
		}
		w.Done, w.Result = true, "executed"
		d.event("complete", map[string]any{"case_id": w.CaseID, "height": height})
		return nil
	}
	if c != nil && (c.Status == st.CaseStatus_CASE_STATUS_REJECTED || c.Status == st.CaseStatus_CASE_STATUS_REVOKED || c.Status == st.CaseStatus_CASE_STATUS_EXPIRED) {
		w.Done, w.Result = true, c.Status.String()
		return nil
	}
	target, err := parseTargetType(d.opts.targetType)
	if err != nil {
		return err
	}
	if c != nil && c.TargetType != target {
		w.Done, w.Result = true, "different_target_type"
		return nil
	}
	if c != nil && target == st.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS {
		if err = d.bindOriginal(ctx, w, c, height); err != nil {
			return err
		}
	}
	if c == nil {
		currentlyActive := false
		for _, a := range active {
			if a.AgentId == d.opts.agentID {
				currentlyActive = true
			}
		}
		if !currentlyActive {
			return nil
		}
		original, e := d.rpc.LookupTx(ctx, w.Hash)
		if e != nil {
			return e
		}
		if target == st.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS && (!original.Found || original.Height > height) {
			return nil
		}
		if original.Found && original.Code != 0 {
			w.Done, w.Result = true, "failed_original"
			return nil
		}
		if target == st.SanctionTargetType_SANCTION_TARGET_TYPE_TX && original.Found {
			w.Done, w.Result = true, "already_finalized"
			return nil
		}
	}
	if w.Review != nil && (w.Review.RiskReport == nil || w.Review.RiskReport.ExpiryHeight <= height+1) {
		w.Review = nil
		d.event("evidence_expired", map[string]any{"case_id": w.CaseID, "height": height})
	}
	if w.Review == nil {
		if len(w.Transfers) == 0 && c != nil {
			if len(c.ReportIds) == 0 {
				return fmt.Errorf("case missing report")
			}
			report := &st.QueryRiskReportResponse{}
			if err = d.query(ctx, height, "RiskReport", &st.QueryRiskReportRequest{ReportId: c.ReportIds[0]}, report); err != nil {
				return err
			}
			if report.Report == nil {
				return fmt.Errorf("missing report")
			}
			r := report.Report
			w.Transfers = []watcher.DecodedTransfer{{MessageIndex: r.MessageIndex, TxHash: w.Hash, Sender: r.Sender, Recipient: r.Recipient, Amount: r.Amount, Denom: r.Denom}}
		}
		if err = d.evaluate(ctx, w, height, params); err != nil {
			return err
		}
	}
	action, err := parseSanctionAction(w.Review.Action)
	if err != nil {
		return err
	}
	if c == nil {
		if action == st.SanctionAction_SANCTION_ACTION_UNSPECIFIED {
			w.Done, w.Result = true, "no_action"
			return nil
		}
		if height < w.SeenHeight+uint64(rank)*2 {
			return nil
		}
		msgs, e := d.openMessages(w)
		if e != nil {
			return e
		}
		id := "open/" + w.CaseID
		if err = d.manager.Enqueue(id, "open", w.CaseID, msgs...); err != nil {
			return err
		}
		var existing txmanager.Intent
		if _, err = d.store.Get("intents", id, &existing); err != nil {
			return err
		}
		if existing.Status == "failed" && expiredFailure(existing) {
			last := existing.Attempts[len(existing.Attempts)-1]
			return d.manager.Recover(ctx, id, height, "re-evaluate expired report", func(txmanager.Intent) ([]sdk.Msg, error) { return msgs, nil }, "expired/"+last.Hash)
		}
		return d.manager.RefreshQueued(id, height, msgs...)
	}
	votes := &st.QuerySanctionVotesResponse{}
	if err = d.query(ctx, height, "SanctionVotes", &st.QuerySanctionVotesRequest{CaseId: w.CaseID}, votes); err != nil {
		return err
	}
	voted := false
	approved := map[string]bool{}
	for _, v := range votes.Votes {
		if v.AgentId == d.opts.agentID {
			voted = true
		}
		if v.Option == st.VoteOption_VOTE_OPTION_APPROVE && v.ApprovedAction == c.RequestedAction {
			approved[v.AgentId] = true
		}
	}
	if d.caseMember(c) && !voted && (c.Status == st.CaseStatus_CASE_STATUS_PENDING || (c.Status == st.CaseStatus_CASE_STATUS_APPROVED && c.RequestedAction == st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER)) {
		option := st.VoteOption_VOTE_OPTION_APPROVE
		if action != c.RequestedAction {
			option = st.VoteOption_VOTE_OPTION_REJECT
		}
		v, e := d.signer.SignVote(d.opts.chainID, &st.SanctionVote{CaseId: w.CaseID, Option: option, ApprovedAction: c.RequestedAction, RationaleHash: w.Review.RiskReport.LlmRationaleHash, SignedHeight: height, ReasonCode: "independent_policy_review"})
		if e != nil {
			return e
		}
		if err = d.manager.Enqueue("vote/"+w.CaseID, "vote", w.CaseID, &st.MsgSubmitSanctionVote{Submitter: d.signer.Address, Vote: v}); err != nil {
			return err
		}
	}
	if c.Status != st.CaseStatus_CASE_STATUS_APPROVED {
		return nil
	}
	if action != c.RequestedAction {
		return nil
	}
	if c.RequestedAction == st.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER && c.UnanimousRequiredForRevert {
		for _, a := range c.Voters {
			if !approved[a.AgentId] {
				return nil
			}
		}
	}
	if w.ApprovedHeight == 0 {
		w.ApprovedHeight = height
		if err = d.store.Put("work", autoCaseID(w.Hash), w); err != nil {
			return err
		}
	}
	if height < w.ApprovedHeight+uint64(rank)*2 {
		return nil
	}
	return d.manager.Enqueue("execute/"+w.CaseID, "execute", w.CaseID, &st.MsgExecuteSanction{Executor: d.signer.Address, CaseId: w.CaseID, ResultMessage: "durable autonomous agent"})
}

func (d *daemon) caseMember(c *st.SanctionCase) bool {
	if c == nil || c.VotingSnapshotVersion != 1 {
		return false
	}
	for _, a := range c.Voters {
		if a.AgentId == d.opts.agentID && a.SignerAddress == d.signer.Address && bytes.Equal(a.PublicKey, d.signer.PublicKey()) {
			return true
		}
	}
	return false
}

// Independently bind a proposed monetary action to a successful original bank send.
func (d *daemon) bindOriginal(ctx context.Context, w *daemonWork, c *st.SanctionCase, height uint64) error {
	original, err := d.rpc.LookupTx(ctx, w.Hash)
	if err != nil {
		return err
	}
	if !original.Found || original.Code != 0 || original.Height > height {
		return fmt.Errorf("case original is not a confirmed successful transfer")
	}
	if len(c.ReportIds) == 0 {
		return fmt.Errorf("case missing report")
	}
	response := &st.QueryRiskReportResponse{}
	if err = d.query(ctx, height, "RiskReport", &st.QueryRiskReportRequest{ReportId: c.ReportIds[0]}, response); err != nil {
		return err
	}
	r := response.Report
	if r == nil {
		return fmt.Errorf("case missing primary report")
	}
	transfers, err := watcher.DecodeTransfers(watcher.PendingTx{Hash: w.Hash, Bytes: original.Tx})
	if err != nil {
		return err
	}
	for _, transfer := range transfers {
		if transfer.MessageIndex == r.MessageIndex && transfer.Sender == r.Sender && transfer.Recipient == r.Recipient && transfer.Amount == r.Amount && transfer.Denom == r.Denom && bytes.Equal(c.TxHash, r.TxHash) && c.TargetAddress == r.Recipient {
			w.Transfers = []watcher.DecodedTransfer{transfer}
			if w.Review != nil {
				old := w.Review.RiskReport
				if old == nil || old.MessageIndex != r.MessageIndex || old.Sender != r.Sender || old.Recipient != r.Recipient || old.Amount != r.Amount || old.Denom != r.Denom {
					w.Review = nil
				}
			}
			return nil
		}
	}
	return fmt.Errorf("case report does not match original bank transfer; manual review required")
}
