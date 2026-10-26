package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/yanagihalab/G503/dev/agent/internal/agentapi"
	"github.com/yanagihalab/G503/dev/agent/internal/agentconfig"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	"github.com/yanagihalab/G503/dev/agent/internal/llmclient"
	"github.com/yanagihalab/G503/dev/agent/internal/policy"
	"github.com/yanagihalab/G503/dev/agent/internal/riskclient"
	"github.com/yanagihalab/G503/dev/agent/internal/signer"
	"github.com/yanagihalab/G503/dev/agent/internal/txmanager"
	"github.com/yanagihalab/G503/dev/agent/internal/watcher"
	sanctiontypes "github.com/yanagihalab/G503/x/sanction/types"
)

type inputTx struct {
	MessageIndex uint32 `json:"message_index"`
	TxHash       string `json:"tx_hash"`
	Sender       string `json:"sender"`
	Recipient    string `json:"recipient"`
	Amount       string `json:"amount"`
	Denom        string `json:"denom"`
	Confirmed    bool   `json:"confirmed"`
}

type output struct {
	Risk        riskclient.RiskResponse     `json:"risk"`
	Explanation llmclient.ExplainResponse   `json:"explanation"`
	Decision    *agentapi.EvaluateResponse  `json:"decision,omitempty"`
	Action      string                      `json:"action"`
	RiskReport  *sanctiontypes.RiskReport   `json:"risk_report,omitempty"`
	Vote        *sanctiontypes.SanctionVote `json:"vote,omitempty"`
	AgentInfo   *sanctiontypes.AgentInfo    `json:"agent_info,omitempty"`
	Broadcast   *broadcastOutput            `json:"broadcast,omitempty"`
}

type broadcastOutput struct {
	Messages []string                    `json:"messages"`
	Result   chainclient.BroadcastResult `json:"result"`
}

func main() {
	var (
		txPath         = flag.String("tx", "", "path to tx metadata JSON")
		configPath     = flag.String("config", "", "optional sanction-agent JSON config")
		riskURL        = flag.String("risk-url", "http://127.0.0.1:8081", "risk service URL")
		riskProvider   = flag.String("risk-provider", "mock", "risk provider: mock or chainalysis")
		riskAPIKey     = flag.String("risk-api-key", os.Getenv("CHAINALYSIS_API_KEY"), "risk provider API key; defaults to CHAINALYSIS_API_KEY")
		llmURL         = flag.String("llm-url", "http://127.0.0.1:8082", "LLM service URL")
		llmProvider    = flag.String("llm-provider", "mock", "LLM provider: mock or ollama")
		llmModel       = flag.String("llm-model", "llama3.1", "LLM model name for ollama")
		agentAPIURL    = flag.String("agent-api-url", "", "optional AI agent evaluation API URL")
		agentAPIToken  = flag.String("agent-api-token", os.Getenv("G503_AGENT_API_TOKEN"), "AI agent API bearer token; defaults to G503_AGENT_API_TOKEN")
		agentAPIRole   = flag.String("agent-api-role", "validator-agent", "AI agent API RBAC role header")
		mtlsCertFile   = flag.String("mtls-cert-file", "", "optional client mTLS certificate file for agent API")
		mtlsKeyFile    = flag.String("mtls-key-file", "", "optional client mTLS key file for agent API")
		mtlsCAFile     = flag.String("mtls-ca-file", "", "optional CA file for agent API mTLS")
		keyFile        = flag.String("key-file", "", "file containing secp256k1 private key hex")
		trustedAddrs   = flag.String("trusted-addresses", "", "comma-separated trusted addresses for agent API policy calibration")
		blockedAddrs   = flag.String("blocked-addresses", "", "comma-separated blocked addresses for agent API policy calibration")
		txConfirmed    = flag.Bool("tx-confirmed", false, "mark tx as already confirmed for agent API action selection")
		chainID        = flag.String("chain-id", "sanction-demo-1", "chain ID used for signing")
		agentID        = flag.String("agent-id", "", "agent id for signing")
		privKeyHex     = flag.String("privkey-hex", "", "secp256k1 private key hex for signing")
		caseID         = flag.String("case-id", "", "case id for optional signed vote")
		height         = flag.Uint64("height", 1, "observed and signed height")
		expiryHeight   = flag.Uint64("expiry-height", 101, "risk report expiry height")
		targetType     = flag.String("target-type", "tx", "sanction target type: tx or address")
		broadcast      = flag.Bool("broadcast", false, "broadcast generated sanction messages as a Cosmos SDK tx")
		rpcURL         = flag.String("rpc-url", "http://127.0.0.1:26657", "CometBFT RPC URL for tx broadcast")
		execute        = flag.Bool("execute", false, "append MsgExecuteSanction after voting; useful for single-agent PoC")
		voteOnly       = flag.Bool("vote-only", false, "broadcast only MsgSubmitSanctionVote for an existing case")
		executeOnly    = flag.Bool("execute-only", false, "broadcast only MsgExecuteSanction for an approved case")
		voteAction     = flag.String("vote-action", "block_tx", "approved action for -vote-only: block_tx, freeze_address, escrow_funds, or revert_transfer")
		rationaleHash  = flag.String("rationale-hash-hex", "", "optional rationale hash hex for -vote-only")
		watch          = flag.Bool("watch", false, "poll pending CometBFT txs, decode bank sends, and screen them")
		daemon         = flag.Bool("daemon", false, "durable autonomous proposal/vote/execute daemon")
		stateDir       = flag.String("state-dir", "", "owner-only durable agent journal directory (required for daemon)")
		daemonStatus   = flag.Bool("daemon-status", false, "export a stopped daemon journal as JSON")
		journalBackup  = flag.String("journal-backup", "", "offline backup archive path (never overwritten)")
		journalRestore = flag.String("journal-restore", "", "offline archive to restore into an absent agent.db")
		backupChecksum = flag.String("backup-sha256", "", "trusted SHA-256 required for restore")
		genesisBinding = flag.String("journal-genesis-hash", "", "trusted genesis fingerprint for restore or explicit legacy binding")
		printGenesis   = flag.Bool("print-genesis-hash", false, "print the RPC genesis fingerprint without opening journal or reading key")
		retryIntent    = flag.String("retry-intent", "", "explicit failed intent to reconcile and retry when starting daemon")
		retryReason    = flag.String("retry-reason", "", "operator reason for the explicit retry")
		retryRequestID = flag.String("retry-request-id", "", "stable idempotency key for a recovery request; defaults to reason hash")
		leaseTTL       = flag.Uint64("lease-ttl-blocks", 24, "signing ownership TTL in blocks (6..100)")
		leaseWait      = flag.Duration("lease-wait", 0, "explicit startup wait for the previous signing owner's lease to expire")
		startHeight    = flag.Uint64("start-height", 0, "initial block replay height; zero starts at current tip, restart resumes cursor")
		pollInterval   = flag.Duration("poll-interval", time.Second, "pending tx polling interval")
		pollLimit      = flag.Int("poll-limit", 100, "maximum pending txs to fetch per poll")
		accountNumber  = flag.Uint64("account-number", 0, "Cosmos account number for signed tx broadcast")
		sequence       = flag.Uint64("sequence", 0, "Cosmos account sequence for signed tx broadcast")
		gasLimit       = flag.Uint64("gas", 500000, "gas limit for signed tx broadcast")
		fee            = flag.String("fee", "0stake", "fee amount for signed tx broadcast, for example 1000stake")
	)
	flag.Parse()
	explicitFlags := map[string]string{}
	flag.Visit(func(f *flag.Flag) { explicitFlags[f.Name] = f.Value.String() })

	cfg, err := agentconfig.Load(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	applyConfigDefaults(
		cfg,
		riskURL,
		riskProvider,
		riskAPIKey,
		llmURL,
		llmProvider,
		llmModel,
		agentAPIURL,
		agentAPIToken,
		agentAPIRole,
		mtlsCertFile,
		mtlsKeyFile,
		mtlsCAFile,
		chainID,
		agentID,
		privKeyHex,
		keyFile,
		accountNumber,
		sequence,
		gasLimit,
		fee,
	)
	for name, value := range explicitFlags {
		if err := flag.Set(name, value); err != nil {
			log.Fatal(err)
		}
	}
	if _, explicit := explicitFlags["rpc-url"]; !explicit && cfg.RPCURL != "" {
		*rpcURL = cfg.RPCURL
	}
	if _, ok := explicitFlags["daemon"]; !ok && cfg.Daemon.Enabled {
		*daemon = true
	}
	if _, ok := explicitFlags["state-dir"]; !ok && cfg.Daemon.StateDir != "" {
		*stateDir = cfg.Daemon.StateDir
	}
	if _, ok := explicitFlags["start-height"]; !ok && cfg.Daemon.StartHeight != 0 {
		*startHeight = cfg.Daemon.StartHeight
	}
	maintenance := 0
	if *daemonStatus {
		maintenance++
	}
	if *journalBackup != "" {
		maintenance++
	}
	if *journalRestore != "" {
		maintenance++
	}
	if *printGenesis {
		maintenance++
	}
	if maintenance > 1 || (maintenance > 0 && *retryIntent != "") {
		log.Fatal("journal commands and retry-intent are mutually exclusive")
	}
	if *printGenesis {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		hash, err := chainclient.New(*rpcURL, 10*time.Second).GenesisHash(ctx, *chainID)
		if err != nil {
			log.Fatal(err)
		}
		if err = json.NewEncoder(os.Stdout).Encode(map[string]string{"chain_id": *chainID, "genesis_hash": hash}); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *journalBackup != "" {
		if *stateDir == "" {
			log.Fatal("journal-backup requires state-dir")
		}
		result, err := txmanager.Backup(filepath.Join(*stateDir, "agent.db"), *journalBackup)
		if err != nil {
			log.Fatal(err)
		}
		if err = json.NewEncoder(os.Stdout).Encode(result); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *daemonStatus {
		if *stateDir == "" {
			log.Fatal("daemon-status requires state-dir")
		}
		snapshot, err := txmanager.Snapshot(filepath.Join(*stateDir, "agent.db"))
		if err != nil {
			log.Fatal(err)
		}
		if err = json.NewEncoder(os.Stdout).Encode(snapshot); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *daemon {
		*broadcast = true
	}
	if *daemon && (*watch || *voteOnly || *executeOnly || *txPath != "") {
		log.Fatal("daemon cannot be combined with watch or one-shot modes")
	}
	if _, explicit := explicitFlags["privkey-hex"]; !explicit && *keyFile != "" {
		key, err := agentconfig.ReadKeyFile(*keyFile)
		if err != nil {
			log.Fatal(err)
		}
		*privKeyHex = key
	}
	if *journalRestore == "" && *agentAPIURL != "" && (*agentID == "" || *agentAPIToken == "") {
		log.Fatal("agent-id and agent API token are required when agent-api-url is set")
	}
	if *broadcast && (*agentID == "" || *privKeyHex == "") {
		log.Fatal("broadcast requires agent-id and a private key")
	}
	if _, explicit := explicitFlags["trusted-addresses"]; !explicit && len(cfg.AgentAPI.Trusted) > 0 {
		*trustedAddrs = strings.Join(cfg.AgentAPI.Trusted, ",")
	}
	if _, explicit := explicitFlags["blocked-addresses"]; !explicit && len(cfg.AgentAPI.Blocked) > 0 {
		*blockedAddrs = strings.Join(cfg.AgentAPI.Blocked, ",")
	}

	opts := processOptions{
		leaseTTL:    *leaseTTL,
		leaseWait:   *leaseWait,
		retryIntent: *retryIntent, retryReason: *retryReason, retryRequestID: *retryRequestID, genesisBinding: *genesisBinding,
		riskURL:       *riskURL,
		riskProvider:  *riskProvider,
		riskAPIKey:    *riskAPIKey,
		llmURL:        *llmURL,
		llmProvider:   *llmProvider,
		llmModel:      *llmModel,
		agentAPIURL:   *agentAPIURL,
		agentAPIToken: *agentAPIToken,
		agentAPIRole:  *agentAPIRole,
		mtlsCertFile:  *mtlsCertFile,
		mtlsKeyFile:   *mtlsKeyFile,
		mtlsCAFile:    *mtlsCAFile,
		trustedAddrs:  splitCSV(*trustedAddrs),
		blockedAddrs:  splitCSV(*blockedAddrs),
		txConfirmed:   *txConfirmed,
		chainID:       *chainID,
		agentID:       *agentID,
		privKeyHex:    *privKeyHex,
		caseID:        *caseID,
		height:        *height,
		expiryHeight:  *expiryHeight,
		targetType:    *targetType,
		broadcast:     *broadcast,
		rpcURL:        *rpcURL,
		execute:       *execute,
		accountNumber: *accountNumber,
		sequence:      *sequence,
		gasLimit:      *gasLimit,
		fee:           *fee,
	}
	if *journalRestore != "" {
		if *stateDir == "" {
			log.Fatal("journal-restore requires state-dir")
		}
		s, err := signer.New(opts.agentID, opts.privKeyHex)
		if err != nil {
			log.Fatal(err)
		}
		if err = txmanager.Restore(*journalRestore, filepath.Join(*stateDir, "agent.db"), journalIdentity(opts, s), *genesisBinding, *backupChecksum); err != nil {
			log.Fatal(err)
		}
		if err = json.NewEncoder(os.Stdout).Encode(map[string]any{"restored": true, "state_dir": *stateDir}); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *retryIntent != "" && !*daemon {
		log.Fatal("retry-intent requires daemon mode")
	}
	if *voteOnly || *executeOnly {
		if *caseID == "" {
			log.Fatal("-case-id is required for -vote-only and -execute-only")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		result, err := broadcastControlTx(ctx, opts, *voteOnly, *executeOnly, *voteAction, *rationaleHash)
		if err != nil {
			log.Fatal(err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *watch {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := runWatch(ctx, opts, *pollInterval, *pollLimit, os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
			log.Fatal(err)
		}
		return
	}
	if *daemon {
		if *voteOnly || *executeOnly || *txPath != "" {
			log.Fatal("daemon cannot be combined with one-shot modes")
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := runDaemon(ctx, opts, *stateDir, *startHeight, *pollInterval, *pollLimit, os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
			log.Fatal(err)
		}
		return
	}

	if *txPath == "" {
		log.Fatal("-tx is required unless -watch is set")
	}
	tx, err := readTx(*txPath)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := processTx(ctx, tx, opts)
	if err != nil {
		log.Fatal(err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		log.Fatal(err)
	}
}

func runWatch(ctx context.Context, opts processOptions, interval time.Duration, limit int, writer io.Writer) error {
	if opts.targetType != "" && opts.targetType != "tx" {
		return fmt.Errorf("watch requires target-type tx for pending transfers")
	}
	if opts.expiryHeight <= opts.height {
		return fmt.Errorf("expiry-height must be greater than height")
	}
	ttl := opts.expiryHeight - opts.height
	if opts.broadcast && opts.privKeyHex == "" {
		return fmt.Errorf("watch broadcast requires a signing key")
	}
	if opts.privKeyHex != "" {
		if _, err := signer.New(opts.agentID, opts.privKeyHex); err != nil {
			return err
		}
	}
	enc := json.NewEncoder(writer)
	client := watcher.New(opts.rpcURL, 10*time.Second)
	client.OnError = func(err error) { log.Printf("watch retry: %v", err) }
	rpc := chainclient.New(opts.rpcURL, 10*time.Second)
	return client.PollUnconfirmed(ctx, interval, limit, func(ctx context.Context, pending watcher.PendingTx) error {
		transfers, err := watcher.DecodeTransfers(pending)
		if err != nil || len(transfers) == 0 {
			return err
		}
		current := opts
		if opts.privKeyHex != "" {
			height, err := rpc.CurrentHeight(ctx)
			if err != nil {
				return err
			}
			if height > ^uint64(0)-ttl {
				return fmt.Errorf("evidence expiry height overflows")
			}
			current.height, current.expiryHeight = height, height+ttl
		}
		result, err := processPendingTransfers(ctx, transfers, current)
		if err != nil {
			return err
		}
		if result.Broadcast != nil {
			opts.sequence++
		}
		return enc.Encode(result)
	})
}

func processPendingTransfers(ctx context.Context, transfers []watcher.DecodedTransfer, opts processOptions) (output, error) {
	if len(transfers) == 0 {
		return output{}, fmt.Errorf("no transfers to screen")
	}
	reviewOpts := opts
	reviewOpts.broadcast = false
	var selected output
	var selectedAction sanctiontypes.SanctionAction
	for i, transfer := range transfers {
		result, err := processTx(ctx, inputTx{MessageIndex: transfer.MessageIndex, TxHash: transfer.TxHash, Sender: transfer.Sender, Recipient: transfer.Recipient, Amount: transfer.Amount, Denom: transfer.Denom}, reviewOpts)
		if err != nil {
			return output{}, err
		}
		action, err := parseSanctionAction(result.Action)
		if err != nil {
			return output{}, err
		}
		if i == 0 || action > selectedAction || (action == selectedAction && result.Risk.RiskScore > selected.Risk.RiskScore) {
			selected, selectedAction = result, action
		}
	}
	if opts.broadcast && selected.Vote != nil {
		action, err := parseSanctionAction(selected.Action)
		if err != nil {
			return output{}, err
		}
		targetType, err := parseTargetType(opts.targetType)
		if err != nil {
			return output{}, err
		}
		result, err := broadcastMessages(ctx, opts, selected.RiskReport.Submitter, selected.Vote.CaseId, selected.RiskReport, selected.Vote, action, targetType, opts.execute)
		if err != nil {
			return output{}, err
		}
		selected.Broadcast = &result
	}
	return selected, nil
}

func broadcastControlTx(ctx context.Context, opts processOptions, voteOnly bool, executeOnly bool, actionName string, rationaleHashHex string) (broadcastOutput, error) {
	s, err := signer.New(opts.agentID, opts.privKeyHex)
	if err != nil {
		return broadcastOutput{}, err
	}
	msgs := []sdk.Msg{}
	names := []string{}
	if voteOnly {
		action, err := parseSanctionAction(actionName)
		if err != nil {
			return broadcastOutput{}, err
		}
		rationaleHash := []byte{}
		if rationaleHashHex != "" {
			rationaleHash, err = hex.DecodeString(rationaleHashHex)
			if err != nil {
				return broadcastOutput{}, err
			}
		}
		vote, err := s.SignVote(opts.chainID, &sanctiontypes.SanctionVote{
			CaseId:         opts.caseID,
			Option:         sanctiontypes.VoteOption_VOTE_OPTION_APPROVE,
			ApprovedAction: action,
			ReasonCode:     "policy_match",
			RationaleHash:  rationaleHash,
			SignedHeight:   opts.height,
		})
		if err != nil {
			return broadcastOutput{}, err
		}
		msgs = append(msgs, &sanctiontypes.MsgSubmitSanctionVote{Submitter: s.Address, Vote: vote})
		names = append(names, "MsgSubmitSanctionVote")
	}
	if executeOnly {
		msgs = append(msgs, &sanctiontypes.MsgExecuteSanction{
			Executor:      s.Address,
			CaseId:        opts.caseID,
			ResultMessage: "executed by sanction-agent control tx",
		})
		names = append(names, "MsgExecuteSanction")
	}
	if len(msgs) == 0 {
		return broadcastOutput{}, fmt.Errorf("no control message requested")
	}
	fee, err := sdk.ParseCoinsNormalized(opts.fee)
	if err != nil {
		return broadcastOutput{}, err
	}
	result, err := chainclient.New(opts.rpcURL, 10*time.Second).BroadcastSignedMsgs(ctx, chainclient.SignOptions{
		ChainID:       opts.chainID,
		PrivateKeyHex: opts.privKeyHex,
		AccountNumber: opts.accountNumber,
		Sequence:      opts.sequence,
		GasLimit:      opts.gasLimit,
		FeeAmount:     fee,
	}, msgs...)
	if err != nil {
		return broadcastOutput{}, err
	}
	return broadcastOutput{Messages: names, Result: result}, nil
}

func parseSanctionAction(name string) (sanctiontypes.SanctionAction, error) {
	switch name {
	case "", "none", "unspecified":
		return sanctiontypes.SanctionAction_SANCTION_ACTION_UNSPECIFIED, nil
	case "watch":
		return sanctiontypes.SanctionAction_SANCTION_ACTION_WATCH, nil
	case "block_tx":
		return sanctiontypes.SanctionAction_SANCTION_ACTION_BLOCK_TX, nil
	case "freeze_address":
		return sanctiontypes.SanctionAction_SANCTION_ACTION_FREEZE_ADDRESS, nil
	case "escrow_funds":
		return sanctiontypes.SanctionAction_SANCTION_ACTION_ESCROW_FUNDS, nil
	case "revert_transfer":
		return sanctiontypes.SanctionAction_SANCTION_ACTION_REVERT_TRANSFER, nil
	default:
		return sanctiontypes.SanctionAction_SANCTION_ACTION_UNSPECIFIED, fmt.Errorf("unsupported sanction action: %s", name)
	}
}

func applyConfigDefaults(
	cfg agentconfig.Config,
	riskURL *string,
	riskProvider *string,
	riskAPIKey *string,
	llmURL *string,
	llmProvider *string,
	llmModel *string,
	agentAPIURL *string,
	agentAPIToken *string,
	agentAPIRole *string,
	mtlsCertFile *string,
	mtlsKeyFile *string,
	mtlsCAFile *string,
	chainID *string,
	agentID *string,
	privKeyHex *string,
	keyFile *string,
	accountNumber *uint64,
	sequence *uint64,
	gasLimit *uint64,
	fee *string,
) {
	if cfg.RiskURL != "" {
		*riskURL = cfg.RiskURL
	}
	if cfg.RiskProvider != "" {
		*riskProvider = cfg.RiskProvider
	}
	if cfg.RiskAPIKey() != "" {
		*riskAPIKey = cfg.RiskAPIKey()
	}
	if cfg.LLMURL != "" {
		*llmURL = cfg.LLMURL
	}
	if cfg.LLMProvider != "" {
		*llmProvider = cfg.LLMProvider
	}
	if cfg.LLMModel != "" {
		*llmModel = cfg.LLMModel
	}
	if cfg.AgentAPI.URL != "" {
		*agentAPIURL = cfg.AgentAPI.URL
	}
	if cfg.AgentAPIToken() != "" {
		*agentAPIToken = cfg.AgentAPIToken()
	}
	if cfg.AgentAPI.Role != "" {
		*agentAPIRole = cfg.AgentAPI.Role
	}
	if cfg.AgentAPI.MTLSCertFile != "" {
		*mtlsCertFile = cfg.AgentAPI.MTLSCertFile
	}
	if cfg.AgentAPI.MTLSKeyFile != "" {
		*mtlsKeyFile = cfg.AgentAPI.MTLSKeyFile
	}
	if cfg.AgentAPI.MTLSCAFile != "" {
		*mtlsCAFile = cfg.AgentAPI.MTLSCAFile
	}
	if cfg.ChainID != "" {
		*chainID = cfg.ChainID
	}
	if cfg.AgentID != "" {
		*agentID = cfg.AgentID
	}
	if cfg.PrivateKeyHex != "" {
		*privKeyHex = cfg.PrivateKeyHex
	}
	if cfg.KeyFile != "" {
		*keyFile = cfg.KeyFile
	}
	if cfg.Broadcast.AccountNumber != 0 {
		*accountNumber = cfg.Broadcast.AccountNumber
	}
	if cfg.Broadcast.Sequence != 0 {
		*sequence = cfg.Broadcast.Sequence
	}
	if cfg.Broadcast.Gas != 0 {
		*gasLimit = cfg.Broadcast.Gas
	}
	if cfg.Broadcast.Fee != "" {
		*fee = cfg.Broadcast.Fee
	}
}

func splitCSV(value string) []string {
	out := []string{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

type processOptions struct {
	leaseTTL       uint64
	leaseWait      time.Duration
	retryIntent    string
	retryRequestID string
	retryReason    string
	genesisBinding string
	thresholds     *policy.Thresholds
	riskURL        string
	riskProvider   string
	riskAPIKey     string
	llmURL         string
	llmProvider    string
	llmModel       string
	agentAPIURL    string
	agentAPIToken  string
	agentAPIRole   string
	mtlsCertFile   string
	mtlsKeyFile    string
	mtlsCAFile     string
	trustedAddrs   []string
	blockedAddrs   []string
	txConfirmed    bool
	chainID        string
	agentID        string
	privKeyHex     string
	caseID         string
	height         uint64
	expiryHeight   uint64
	targetType     string
	broadcast      bool
	rpcURL         string
	execute        bool
	accountNumber  uint64
	sequence       uint64
	gasLimit       uint64
	fee            string
}

func processTx(ctx context.Context, tx inputTx, opts processOptions) (output, error) {
	var result output
	thresholds := policy.DefaultThresholds()
	if opts.thresholds != nil {
		thresholds = *opts.thresholds
	}
	risk, err := riskclient.NewWithProvider(opts.riskURL, opts.riskProvider, opts.riskAPIKey, 5*time.Second).ScreenTx(ctx, riskclient.TxScreenRequest{
		TxHash:    tx.TxHash,
		Sender:    tx.Sender,
		Recipient: tx.Recipient,
		Amount:    tx.Amount,
		Denom:     tx.Denom,
	})
	if err != nil {
		return result, err
	}

	targetType, err := parseTargetType(opts.targetType)
	if err != nil {
		return result, err
	}
	rawRiskScore := risk.RiskScore
	rawRiskResponseID := risk.ResponseID
	var agentDecision *agentapi.EvaluateResponse
	var agentDecisionJSON []byte
	if opts.agentAPIURL != "" {
		agentClient, err := agentapi.New(opts.agentAPIURL, opts.agentAPIToken, opts.agentAPIRole, 10*time.Second, agentapi.TLSConfig{
			CertFile: opts.mtlsCertFile,
			KeyFile:  opts.mtlsKeyFile,
			CAFile:   opts.mtlsCAFile,
		})
		if err != nil {
			return result, err
		}
		decision, rawDecision, err := agentClient.Evaluate(ctx, agentapi.EvaluateRequest{
			AgentID:    opts.agentID,
			TargetType: opts.targetType,
			Tx: agentapi.TxInput{
				TxHash:    tx.TxHash,
				Sender:    tx.Sender,
				Recipient: tx.Recipient,
				Amount:    tx.Amount,
				Denom:     tx.Denom,
				Confirmed: opts.txConfirmed || tx.Confirmed,
			},
			Risk: agentapi.RiskInput{
				Source:       risk.Source,
				ResponseID:   risk.ResponseID,
				Address:      risk.Address,
				Score:        risk.RiskScore,
				Categories:   risk.RiskCategories,
				ExposureType: risk.ExposureType,
			},
			Policy: agentapi.PolicyInput{
				WatchThreshold:   thresholds.WatchThreshold,
				BlockThreshold:   thresholds.BlockThreshold,
				FreezeThreshold:  thresholds.FreezeThreshold,
				RevertThreshold:  thresholds.RevertThreshold,
				HighRisk:         thresholds.HighRiskCategories,
				CriticalRisk:     []string{"sanctioned", "terrorist_financing", "ransomware"},
				TrustedAddresses: opts.trustedAddrs,
				BlockedAddresses: opts.blockedAddrs,
			},
		})
		if err != nil {
			return result, err
		}
		agentDecision = &decision
		agentDecisionJSON = rawDecision
		risk.RiskScore = decision.CalibratedScore
		risk.ResponseID = rawRiskResponseID + "|agent-decision:" + decision.DecisionID
	}

	explanation, err := llmclient.New(opts.llmURL, opts.llmProvider, opts.llmModel, 10*time.Second).Explain(ctx, llmclient.ExplainRequest{
		Task:          "risk_explanation",
		PolicyVersion: "poc-v1",
		Tx: map[string]string{
			"hash":      tx.TxHash,
			"sender":    tx.Sender,
			"recipient": tx.Recipient,
			"amount":    tx.Amount,
			"denom":     tx.Denom,
		},
		Risk: llmclient.RiskInput{
			Source:       risk.Source,
			Score:        risk.RiskScore,
			Categories:   risk.RiskCategories,
			ExposureType: risk.ExposureType,
		},
	})
	if err != nil {
		return result, err
	}

	action := policy.Action(
		risk.RiskScore,
		risk.RiskCategories,
		targetType,
		thresholds,
	)
	if agentDecision != nil {
		if agentDecision.AgentID != opts.agentID || agentDecision.DecisionID == "" || agentDecision.CalibratedScore > 100 {
			return result, fmt.Errorf("agent API returned an invalid decision identity or score")
		}
		if agentDecision.EvidenceStatus != "complete" {
			return result, fmt.Errorf("agent API evidence is %s; manual review is required before signing", agentDecision.EvidenceStatus)
		}
		agentAction, err := parseSanctionAction(agentDecision.Action)
		if err != nil {
			return result, err
		}
		if agentAction != action {
			return result, fmt.Errorf("agent API action %s conflicts with chain policy action %s", agentDecision.Action, sanctiontypes.ActionName(action))
		}
		approve := action != sanctiontypes.SanctionAction_SANCTION_ACTION_UNSPECIFIED
		if agentDecision.Approved != approve || (approve && agentDecision.VoteOption != "approve") || (!approve && agentDecision.VoteOption != "reject") {
			return result, fmt.Errorf("agent API returned inconsistent action and vote")
		}
	}

	result = output{
		Risk:        risk,
		Explanation: explanation,
		Decision:    agentDecision,
		Action:      sanctiontypes.ActionName(action),
	}

	if opts.agentID != "" && opts.privKeyHex != "" {
		s, err := signer.New(opts.agentID, opts.privKeyHex)
		if err != nil {
			return result, err
		}
		txHash, err := hex.DecodeString(tx.TxHash)
		if err != nil {
			return result, fmt.Errorf("tx_hash must be hex when signing: %w", err)
		}
		rationaleHash := signer.RationaleHash(explanation.Rationale, explanation.Caveats)
		if agentDecision != nil {
			rationaleHash = signer.EvidenceHash([]byte(agentDecision.Rationale), agentDecisionJSON)
		}
		riskJSON, err := json.Marshal(risk)
		if err != nil {
			return result, err
		}
		explanationJSON, err := json.Marshal(explanation)
		if err != nil {
			return result, err
		}
		report := &sanctiontypes.RiskReport{
			MessageIndex:     tx.MessageIndex,
			ReportId:         "report-" + tx.TxHash,
			TxHash:           txHash,
			Sender:           tx.Sender,
			Recipient:        tx.Recipient,
			Denom:            tx.Denom,
			Amount:           tx.Amount,
			Source:           risk.Source,
			SourceResponseId: risk.ResponseID,
			RiskScore:        risk.RiskScore,
			RiskCategories:   risk.RiskCategories,
			ExposureType:     risk.ExposureType,
			ObservedHeight:   opts.height,
			ExpiryHeight:     opts.expiryHeight,
			PolicyVersion:    policyVersion(agentDecision),
			EvidenceHash:     signer.EvidenceHash([]byte(tx.TxHash), []byte(rawRiskResponseID), []byte(fmt.Sprintf("raw_score=%d", rawRiskScore)), riskJSON, explanationJSON, agentDecisionJSON),
			LlmRationaleHash: rationaleHash,
		}
		report, err = s.SignRiskReport(opts.chainID, report)
		if err != nil {
			return result, err
		}
		result.RiskReport = report
		result.AgentInfo = &sanctiontypes.AgentInfo{
			AgentId:       opts.agentID,
			SignerAddress: s.Address,
			PublicKey:     s.PublicKey(),
			VotingPower:   1,
			Active:        true,
		}
		effectiveCaseID := opts.caseID
		if effectiveCaseID == "" && action != sanctiontypes.SanctionAction_SANCTION_ACTION_UNSPECIFIED {
			effectiveCaseID = "case-" + tx.TxHash
		}
		if effectiveCaseID != "" && action != sanctiontypes.SanctionAction_SANCTION_ACTION_UNSPECIFIED {
			vote, err := s.SignVote(opts.chainID, &sanctiontypes.SanctionVote{
				CaseId:         effectiveCaseID,
				Option:         sanctiontypes.VoteOption_VOTE_OPTION_APPROVE,
				ApprovedAction: action,
				ReasonCode:     "policy_match",
				RationaleHash:  rationaleHash,
				SignedHeight:   opts.height,
			})
			if err != nil {
				return result, err
			}
			result.Vote = vote
		}
		if opts.broadcast {
			broadcast, err := broadcastMessages(ctx, opts, s.Address, effectiveCaseID, report, result.Vote, action, targetType, opts.execute)
			if err != nil {
				return result, err
			}
			result.Broadcast = &broadcast
		}
	}
	return result, nil
}

func policyVersion(decision *agentapi.EvaluateResponse) string {
	if decision == nil {
		return "poc-v1"
	}
	return "poc-v1+agent-api:" + decision.DecisionID
}

func readTx(path string) (inputTx, error) {
	var tx inputTx
	file, err := os.Open(path)
	if err != nil {
		return tx, err
	}
	defer file.Close()
	if err := json.NewDecoder(file).Decode(&tx); err != nil {
		return tx, err
	}
	if tx.TxHash == "" || tx.Recipient == "" {
		return tx, fmt.Errorf("tx_hash and recipient are required")
	}
	return tx, nil
}

func broadcastMessages(
	ctx context.Context,
	opts processOptions,
	submitter string,
	caseID string,
	report *sanctiontypes.RiskReport,
	vote *sanctiontypes.SanctionVote,
	action sanctiontypes.SanctionAction,
	targetType sanctiontypes.SanctionTargetType,
	execute bool,
) (broadcastOutput, error) {
	msgs := []sdk.Msg{
		&sanctiontypes.MsgSubmitRiskReport{Submitter: submitter, Report: report},
	}
	names := []string{"MsgSubmitRiskReport"}
	if caseID != "" && action != sanctiontypes.SanctionAction_SANCTION_ACTION_UNSPECIFIED {
		msgs = append(msgs, &sanctiontypes.MsgOpenSanctionCase{
			Submitter:       submitter,
			CaseId:          caseID,
			TxHash:          report.TxHash,
			TargetAddress:   report.Recipient,
			TargetType:      targetType,
			RequestedAction: action,
			ReportIds:       []string{report.ReportId},
			PolicyVersion:   report.PolicyVersion,
		})
		names = append(names, "MsgOpenSanctionCase")
	}
	if vote != nil {
		msgs = append(msgs, &sanctiontypes.MsgSubmitSanctionVote{Submitter: submitter, Vote: vote})
		names = append(names, "MsgSubmitSanctionVote")
	}
	if execute && caseID != "" && action != sanctiontypes.SanctionAction_SANCTION_ACTION_UNSPECIFIED {
		msgs = append(msgs, &sanctiontypes.MsgExecuteSanction{
			Executor:      submitter,
			CaseId:        caseID,
			ResultMessage: "executed by sanction-agent",
		})
		names = append(names, "MsgExecuteSanction")
	}
	fee, err := sdk.ParseCoinsNormalized(opts.fee)
	if err != nil {
		return broadcastOutput{}, err
	}
	result, err := chainclient.New(opts.rpcURL, 10*time.Second).BroadcastSignedMsgs(ctx, chainclient.SignOptions{
		ChainID:       opts.chainID,
		PrivateKeyHex: opts.privKeyHex,
		AccountNumber: opts.accountNumber,
		Sequence:      opts.sequence,
		GasLimit:      opts.gasLimit,
		FeeAmount:     fee,
	}, msgs...)
	if err != nil {
		return broadcastOutput{}, err
	}
	return broadcastOutput{Messages: names, Result: result}, nil
}

func parseTargetType(name string) (sanctiontypes.SanctionTargetType, error) {
	switch name {
	case "", "tx":
		return sanctiontypes.SanctionTargetType_SANCTION_TARGET_TYPE_TX, nil
	case "address":
		return sanctiontypes.SanctionTargetType_SANCTION_TARGET_TYPE_ADDRESS, nil
	default:
		return sanctiontypes.SanctionTargetType_SANCTION_TARGET_TYPE_UNSPECIFIED, fmt.Errorf("unsupported target type: %s", name)
	}
}
