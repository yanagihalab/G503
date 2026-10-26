package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/yanagihalab/G503/dev/agent/internal/policy"
	"github.com/yanagihalab/G503/dev/agent/internal/signer"
	"github.com/yanagihalab/G503/dev/agent/internal/txmanager"
	"github.com/yanagihalab/G503/dev/agent/internal/watcher"
	st "github.com/yanagihalab/G503/x/sanction/types"
	"google.golang.org/protobuf/proto"
)

func journalIdentity(opts processOptions, s signer.Signer) string {
	return "v1|" + opts.chainID + "|" + opts.agentID + "|" + s.Address + "|" + opts.targetType
}

func parseAutoCaseID(id string) (string, uint64, error) {
	if !strings.HasPrefix(id, "auto-") || len(id) < 69 {
		return "", 0, fmt.Errorf("invalid automatic case ID")
	}
	hash := id[5:69]
	if b, err := hex.DecodeString(hash); err != nil || len(b) != 32 || hash != strings.ToUpper(hash) {
		return "", 0, fmt.Errorf("invalid automatic tx hash")
	}
	if len(id) == 69 {
		return hash, 0, nil
	}
	if !strings.HasPrefix(id[69:], "-r") {
		return "", 0, fmt.Errorf("invalid recovery round")
	}
	n, err := strconv.ParseUint(id[71:], 10, 64)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != id[71:] {
		return "", 0, fmt.Errorf("invalid recovery round")
	}
	return hash, n, nil
}

func roundID(hash string, n uint64) string {
	if n == 0 {
		return autoCaseID(hash)
	}
	return autoCaseID(hash) + "-r" + strconv.FormatUint(n, 10)
}

func (d *daemon) validateRegistry(ctx context.Context, height uint64) error {
	response := &st.QueryAgentsResponse{}
	if err := d.query(ctx, height, "Agents", &st.QueryAgentsRequest{}, response); err != nil {
		return err
	}
	for _, a := range response.Agents {
		if a.AgentId == d.opts.agentID && a.SignerAddress == d.signer.Address && bytes.Equal(a.PublicKey, d.signer.PublicKey()) {
			return nil
		}
	}
	return fmt.Errorf("agent signer identity does not match registry")
}

func (d *daemon) recoveryCase(ctx context.Context, w *daemonWork, height uint64) (*st.SanctionCase, error) {
	response := &st.QuerySanctionCasesByTxResponse{}
	if err := d.query(ctx, height, "SanctionCasesByTx", &st.QuerySanctionCasesByTxRequest{TxHash: w.Hash}, response); err != nil {
		return nil, err
	}
	byID := map[string]*st.SanctionCase{}
	for _, c := range response.SanctionCases {
		byID[c.CaseId] = c
	}
	c := byID[autoCaseID(w.Hash)]
	if c == nil {
		return nil, nil
	}
	var round uint64
	for round < ^uint64(0) {
		next := byID[roundID(w.Hash, round+1)]
		if next == nil || next.PreviousCaseId != c.CaseId {
			break
		}
		c = next
		round++
	}
	if c.ExecutedHeight > 0 || c.Status == st.CaseStatus_CASE_STATUS_REVOKED || c.Status == st.CaseStatus_CASE_STATUS_REJECTED {
		w.CaseID = c.CaseId
		return c, nil
	}
	r := &st.QueryRiskReportResponse{}
	if len(c.ReportIds) == 0 {
		return nil, fmt.Errorf("case missing evidence")
	}
	if err := d.query(ctx, height, "RiskReport", &st.QueryRiskReportRequest{ReportId: c.ReportIds[0]}, r); err != nil {
		return nil, err
	}
	if r.Report == nil {
		return nil, fmt.Errorf("case missing report")
	}
	expired := r.Report.ExpiryHeight <= height+1 || c.Status == st.CaseStatus_CASE_STATUS_EXPIRED || (c.Status == st.CaseStatus_CASE_STATUS_PENDING && c.VotingDeadlineHeight < height)
	if expired && c.TargetType == st.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS {
		// Do not race an execution in the last valid evidence block.
		if r.Report.ExpiryHeight > height && !(c.Status == st.CaseStatus_CASE_STATUS_PENDING && c.VotingDeadlineHeight < height) && c.Status != st.CaseStatus_CASE_STATUS_EXPIRED {
			return c, nil
		}
		if round == ^uint64(0) {
			return nil, fmt.Errorf("recovery round overflow")
		}
		if err := d.bindOriginal(ctx, w, c, height); err != nil {
			return nil, err
		}
		next := roundID(w.Hash, round+1)
		if w.CaseID != next {
			w.Review = nil
			w.ApprovedHeight = 0
			w.SeenHeight = height
			d.event("case_recovery", map[string]any{"previous_case_id": c.CaseId, "case_id": next})
		}
		w.CaseID, w.PreviousCaseID = next, c.CaseId
		return nil, nil
	}
	if w.CaseID != c.CaseId {
		w.CaseID = c.CaseId
		w.PreviousCaseID = c.PreviousCaseId
		w.Review = nil
		w.ApprovedHeight = 0
	}
	return c, nil
}

func (d *daemon) evaluate(ctx context.Context, w *daemonWork, height uint64, params *st.Params) error {
	if len(w.Transfers) == 0 {
		return fmt.Errorf("no original transfer for re-evaluation")
	}
	ttl := d.opts.expiryHeight - d.opts.height
	if params.EvidenceTtlBlocks < ttl {
		ttl = params.EvidenceTtlBlocks
	}
	if ttl < 3 || height > ^uint64(0)-ttl {
		return fmt.Errorf("evidence TTL must allow at least three blocks and not overflow")
	}
	opts := d.opts
	opts.broadcast = false
	opts.height = height
	opts.expiryHeight = height + ttl
	opts.txConfirmed = opts.targetType == "address"
	opts.thresholds = &policy.Thresholds{WatchThreshold: params.WatchThreshold, BlockThreshold: params.BlockThreshold, FreezeThreshold: params.FreezeThreshold, RevertThreshold: params.RevertThreshold, HighRiskCategories: params.HighRiskCategories}
	review, err := processPendingTransfers(ctx, w.Transfers, opts)
	if err != nil {
		return err
	}
	w.Review = &review
	if err = d.store.Put("work", autoCaseID(w.Hash), w); err != nil {
		return err
	}
	d.event("reviewed", map[string]any{"case_id": w.CaseID, "action": review.Action, "observed_height": height, "expiry_height": height + ttl})
	return nil
}

func (d *daemon) openMessages(w *daemonWork) ([]sdk.Msg, error) {
	if w.Review == nil || w.Review.RiskReport == nil {
		return nil, fmt.Errorf("missing fresh review")
	}
	report := proto.Clone(w.Review.RiskReport).(*st.RiskReport)
	report.ReportId = "auto-report-" + w.CaseID + "-h" + strconv.FormatUint(report.ObservedHeight, 10)
	report, err := d.signer.SignRiskReport(d.opts.chainID, report)
	if err != nil {
		return nil, err
	}
	action, err := parseSanctionAction(w.Review.Action)
	if err != nil {
		return nil, err
	}
	if action == st.SanctionAction_SANCTION_ACTION_UNSPECIFIED {
		return nil, txmanager.ErrObsolete
	}
	target, err := parseTargetType(d.opts.targetType)
	if err != nil {
		return nil, err
	}
	return []sdk.Msg{&st.MsgSubmitRiskReport{Submitter: d.signer.Address, Report: report}, &st.MsgOpenSanctionCase{Submitter: d.signer.Address, CaseId: w.CaseID, PreviousCaseId: w.PreviousCaseID, TxHash: report.TxHash, TargetAddress: report.Recipient, TargetType: target, RequestedAction: action, ReportIds: []string{report.ReportId}, PolicyVersion: report.PolicyVersion}}, nil
}

func expiredFailure(i txmanager.Intent) bool {
	if len(i.Attempts) == 0 {
		return false
	}
	a := i.Attempts[len(i.Attempts)-1]
	return (a.Codespace == "sanction" && a.Code == 6) || (a.CheckCodespace == "sanction" && a.CheckCode == 6)
}

func (d *daemon) recoverFailed(ctx context.Context, id string, height uint64, reason string) error {
	return d.manager.Recover(ctx, id, height, reason, func(i txmanager.Intent) ([]sdk.Msg, error) {
		hash, _, err := parseAutoCaseID(i.CaseID)
		if err != nil {
			return nil, err
		}
		var w daemonWork
		found, err := d.store.Get("work", autoCaseID(hash), &w)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("recovery work not found")
		}
		c, err := d.getCase(ctx, i.CaseID, height)
		if err != nil {
			return nil, err
		}
		if c != nil {
			if c.Status == st.CaseStatus_CASE_STATUS_REVOKED || c.Status == st.CaseStatus_CASE_STATUS_REJECTED || c.Status == st.CaseStatus_CASE_STATUS_EXPIRED {
				return nil, txmanager.ErrObsolete
			}
			if err = d.bindOriginal(ctx, &w, c, height); err != nil {
				return nil, err
			}
			report := &st.QueryRiskReportResponse{}
			if err = d.query(ctx, height, "RiskReport", &st.QueryRiskReportRequest{ReportId: c.ReportIds[0]}, report); err != nil {
				return nil, err
			}
			if report.Report == nil {
				return nil, fmt.Errorf("case missing evidence")
			}
			if report.Report.ExpiryHeight <= height || (c.Status == st.CaseStatus_CASE_STATUS_PENDING && c.VotingDeadlineHeight < height) {
				return nil, txmanager.ErrObsolete
			}
		} else {
			if i.Kind != "open" {
				return nil, txmanager.ErrObsolete
			}
			original, e := d.rpc.LookupTx(ctx, hash)
			if e != nil {
				return nil, e
			}
			if !original.Found || original.Code != 0 || original.Height > height || d.opts.targetType != "address" {
				return nil, fmt.Errorf("retry requires a confirmed successful address transfer")
			}
			w.Transfers, e = watcher.DecodeTransfers(watcher.PendingTx{Hash: hash, Bytes: original.Tx})
			if e != nil {
				return nil, e
			}
		}
		params := &st.QueryParamsResponse{}
		if err = d.query(ctx, height, "Params", &st.QueryParamsRequest{}, params); err != nil {
			return nil, err
		}
		if params.Params == nil {
			return nil, fmt.Errorf("missing policy")
		}
		w.CaseID = i.CaseID
		w.Review = nil
		if err = d.evaluate(ctx, &w, height, params.Params); err != nil {
			return nil, err
		}
		if i.Kind == "open" {
			return d.openMessages(&w)
		}
		action, err := parseSanctionAction(w.Review.Action)
		if err != nil {
			return nil, err
		}
		if i.Kind == "vote" {
			if !d.caseMember(c) {
				return nil, txmanager.ErrObsolete
			}
			option := st.VoteOption_VOTE_OPTION_APPROVE
			if action != c.RequestedAction {
				option = st.VoteOption_VOTE_OPTION_REJECT
			}
			vote, err := d.signer.SignVote(d.opts.chainID, &st.SanctionVote{CaseId: c.CaseId, Option: option, ApprovedAction: c.RequestedAction, RationaleHash: w.Review.RiskReport.LlmRationaleHash, SignedHeight: height, ReasonCode: "recovery_independent_review"})
			if err != nil {
				return nil, err
			}
			return []sdk.Msg{&st.MsgSubmitSanctionVote{Submitter: d.signer.Address, Vote: vote}}, nil
		}
		if i.Kind != "execute" || c.Status != st.CaseStatus_CASE_STATUS_APPROVED || action != c.RequestedAction {
			return nil, txmanager.ErrObsolete
		}
		return []sdk.Msg{&st.MsgExecuteSanction{Executor: d.signer.Address, CaseId: c.CaseId, ResultMessage: "operator-authorized durable retry"}}, nil
	}, d.opts.retryRequestID)
}
