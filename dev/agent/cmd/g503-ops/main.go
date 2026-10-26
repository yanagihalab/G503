package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/yanagihalab/G503/dev/agent/internal/agentconfig"
	"github.com/yanagihalab/G503/dev/agent/internal/chainclient"
	"github.com/yanagihalab/G503/dev/agent/internal/signer"
	sanctiontypes "github.com/yanagihalab/G503/x/sanction/types"
)

type configTemplate struct {
	ChainID       string `json:"chain_id"`
	RPCURL        string `json:"rpc_url"`
	RiskURL       string `json:"risk_url"`
	RiskProvider  string `json:"risk_provider"`
	RiskAPIKeyEnv string `json:"risk_api_key_env"`
	LLMURL        string `json:"llm_url"`
	LLMProvider   string `json:"llm_provider"`
	LLMModel      string `json:"llm_model"`
	AgentID       string `json:"agent_id"`
	KeyFile       string `json:"key_file"`
	AgentAPI      struct {
		URL          string   `json:"url"`
		TokenEnv     string   `json:"token_env"`
		Role         string   `json:"role"`
		MTLSCertFile string   `json:"mtls_cert_file"`
		MTLSKeyFile  string   `json:"mtls_key_file"`
		MTLSCAFile   string   `json:"mtls_ca_file"`
		Trusted      []string `json:"trusted_addresses"`
		Blocked      []string `json:"blocked_addresses"`
	} `json:"agent_api"`
	Broadcast struct {
		Gas uint64 `json:"gas"`
		Fee string `json:"fee"`
	} `json:"broadcast"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "config-template":
		err = configTemplateCmd(os.Args[2:])
	case "rotate-key":
		err = rotateKeyCmd(os.Args[2:])
	case "audit-bundle":
		err = auditBundleCmd(os.Args[2:])
	case "revoke-sanction":
		err = revokeSanctionCmd(os.Args[2:])
	case "review-sanction":
		err = sanctionDecisionCmd("review-sanction", os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func configTemplateCmd(args []string) error {
	fs := flag.NewFlagSet("config-template", flag.ExitOnError)
	out := fs.String("out", "", "optional output path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tpl := configTemplate{
		ChainID:       "g503-local-1",
		RPCURL:        "http://127.0.0.1:26657",
		RiskURL:       "http://127.0.0.1:8081",
		RiskProvider:  "mock",
		RiskAPIKeyEnv: "CHAINALYSIS_API_KEY",
		LLMURL:        "http://127.0.0.1:8082",
		LLMProvider:   "mock",
		LLMModel:      "llama3.1",
		AgentID:       "validator-1",
		KeyFile:       "/etc/g503/agent-key.hex",
	}
	tpl.AgentAPI.URL = "http://127.0.0.1:8083"
	tpl.AgentAPI.TokenEnv = "G503_AGENT_API_TOKEN"
	tpl.AgentAPI.Role = "validator-agent"
	tpl.AgentAPI.Trusted = []string{}
	tpl.AgentAPI.Blocked = []string{}
	tpl.Broadcast.Gas = 700000
	tpl.Broadcast.Fee = "0stake"
	return writeJSON(*out, tpl, 0o600)
}

func rotateKeyCmd(args []string) error {
	fs := flag.NewFlagSet("rotate-key", flag.ExitOnError)
	out := fs.String("out", "", "new private key output path")
	agentID := fs.String("agent-id", "validator-rotated", "agent id used to derive and print address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	key := secp256k1.GenPrivKey().Key
	file, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(hex.EncodeToString(key) + "\n"); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	s, err := signer.New(*agentID, hex.EncodeToString(key))
	if err != nil {
		return err
	}
	return writeJSON("", map[string]string{
		"agent_id":             *agentID,
		"new_key_file":         *out,
		"new_signer_address":   s.Address,
		"public_key_hex":       hex.EncodeToString(s.PublicKey()),
		"rotation_next_step":   "new key generated only; existing agent key replacement requires an on-chain update mechanism, which is not implemented",
		"private_key_handling": "file was written mode 0600; move it to managed secret storage before production use",
	}, 0)
}

func auditBundleCmd(args []string) error {
	fs := flag.NewFlagSet("audit-bundle", flag.ExitOnError)
	caseFile := fs.String("case", "", "case query JSON file")
	votesFile := fs.String("votes", "", "votes query JSON file")
	executionFile := fs.String("execution", "", "execution query JSON file")
	decisionFile := fs.String("decision", "", "agent API decision JSON file")
	reason := fs.String("reason", "false-positive-review", "review reason")
	out := fs.String("out", "", "optional output path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	artifacts := map[string]string{}
	contents := map[string][]byte{}
	for name, path := range map[string]string{"case": *caseFile, "votes": *votesFile, "execution": *executionFile, "decision": *decisionFile} {
		if path == "" {
			continue
		}
		bz, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		artifacts[name] = path
		contents[name] = bz
	}
	if len(contents) == 0 {
		return fmt.Errorf("at least one evidence artifact is required")
	}
	payload, err := json.Marshal(contents)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(payload)
	bundle := map[string]any{
		"created_at":        time.Now().UTC().Format(time.RFC3339),
		"workflow":          "appeal-review-revoke",
		"reason":            *reason,
		"artifacts":         artifacts,
		"artifact_contents": contents,
		"hash_format":       "sha256 of canonical JSON artifact_contents (base64 bytes, sorted keys)",
		"evidence_hash_hex": hex.EncodeToString(hash[:]),
		"next_steps": []string{
			"review evidence_hash against source artifacts",
			"broadcast review-sanction with evidence_hash_hex and the sanction authority key",
			"after the review is committed, broadcast revoke-sanction with the authority key",
			"publish audit bundle alongside governance or operator decision",
		},
	}
	return writeJSON(*out, bundle, 0o600)
}

func revokeSanctionCmd(args []string) error {
	return sanctionDecisionCmd("revoke-sanction", args)
}

func sanctionDecisionCmd(command string, args []string) error {
	fs := flag.NewFlagSet(command, flag.ExitOnError)
	rpcURL := fs.String("rpc-url", "http://127.0.0.1:26657", "CometBFT RPC URL")
	chainID := fs.String("chain-id", "g503-local-1", "chain ID")
	privKeyHex := fs.String("privkey-hex", "", "authority private key hex")
	keyFile := fs.String("key-file", "", "authority private key file")
	caseID := fs.String("case-id", "", "case id to revoke")
	reason := fs.String("reason", "false-positive-review", "revoke reason")
	evidenceHex := fs.String("evidence-hash-hex", "", "SHA-256 of review evidence (required for review-sanction)")
	accountNumber := fs.Uint64("account-number", 0, "authority account number")
	sequence := fs.Uint64("sequence", 0, "authority sequence")
	gas := fs.Uint64("gas", 300000, "gas limit")
	feeText := fs.String("fee", "0stake", "fee amount")
	dryRun := fs.Bool("dry-run", false, "print tx plan without broadcast")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *caseID == "" {
		return fmt.Errorf("-case-id is required")
	}
	if *privKeyHex == "" && *keyFile != "" {
		key, err := agentconfig.ReadKeyFile(*keyFile)
		if err != nil {
			return err
		}
		*privKeyHex = key
	}
	s, err := signer.New("authority", *privKeyHex)
	if err != nil {
		return err
	}
	var msg sdk.Msg = &sanctiontypes.MsgRevokeSanction{
		Authority: s.Address,
		CaseId:    *caseID,
		Reason:    *reason,
	}
	if command == "review-sanction" {
		evidence, err := hex.DecodeString(*evidenceHex)
		if err != nil || len(evidence) != sha256.Size {
			return fmt.Errorf("-evidence-hash-hex must be a 32-byte SHA-256 hash")
		}
		msg = &sanctiontypes.MsgReviewSanction{Authority: s.Address, CaseId: *caseID, Reason: *reason, EvidenceHash: evidence}
	}
	fee, err := sdk.ParseCoinsNormalized(*feeText)
	if err != nil {
		return err
	}
	if *dryRun {
		return writeJSON("", map[string]any{
			"authority": s.Address,
			"message":   msg,
		}, 0)
	}
	result, err := chainclient.New(*rpcURL, 10*time.Second).BroadcastSignedMsgs(context.Background(), chainclient.SignOptions{
		ChainID:       *chainID,
		PrivateKeyHex: *privKeyHex,
		AccountNumber: *accountNumber,
		Sequence:      *sequence,
		GasLimit:      *gas,
		FeeAmount:     fee,
	}, msg)
	if err != nil {
		return err
	}
	return writeJSON("", map[string]any{
		"authority": s.Address,
		"case_id":   *caseID,
		"reason":    *reason,
		"broadcast": result,
	}, 0)
}

func writeJSON(path string, value any, perm os.FileMode) error {
	bz, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	bz = append(bz, '\n')
	if path == "" {
		_, err = os.Stdout.Write(bz)
		return err
	}
	if perm == 0 {
		perm = 0o600
	}
	return os.WriteFile(path, bz, perm)
}

func bytesTrimSpace(bz []byte) []byte {
	start := 0
	end := len(bz)
	for start < end && (bz[start] == ' ' || bz[start] == '\n' || bz[start] == '\t' || bz[start] == '\r') {
		start++
	}
	for end > start && (bz[end-1] == ' ' || bz[end-1] == '\n' || bz[end-1] == '\t' || bz[end-1] == '\r') {
		end--
	}
	return bz[start:end]
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: g503-ops <config-template|rotate-key|audit-bundle|review-sanction|revoke-sanction> [flags]")
}
