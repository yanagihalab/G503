package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/yanagihalab/G503/dev/agent/internal/llmclient"
	"github.com/yanagihalab/G503/dev/agent/internal/policy"
	"github.com/yanagihalab/G503/dev/agent/internal/riskclient"
	"github.com/yanagihalab/G503/dev/agent/internal/signer"
	sanctiontypes "github.com/yanagihalab/G503/x/sanction/types"
)

type inputTx struct {
	TxHash    string `json:"tx_hash"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Amount    string `json:"amount"`
	Denom     string `json:"denom"`
}

type output struct {
	Risk        riskclient.RiskResponse     `json:"risk"`
	Explanation llmclient.ExplainResponse   `json:"explanation"`
	Action      string                      `json:"action"`
	RiskReport  *sanctiontypes.RiskReport   `json:"risk_report,omitempty"`
	Vote        *sanctiontypes.SanctionVote `json:"vote,omitempty"`
	AgentInfo   *sanctiontypes.AgentInfo    `json:"agent_info,omitempty"`
}

func main() {
	var (
		txPath       = flag.String("tx", "", "path to tx metadata JSON")
		riskURL      = flag.String("risk-url", "http://127.0.0.1:8081", "risk service URL")
		llmURL       = flag.String("llm-url", "http://127.0.0.1:8082", "LLM service URL")
		llmProvider  = flag.String("llm-provider", "mock", "LLM provider: mock or ollama")
		llmModel     = flag.String("llm-model", "llama3.1", "LLM model name for ollama")
		chainID      = flag.String("chain-id", "sanction-demo-1", "chain ID used for signing")
		agentID      = flag.String("agent-id", "", "agent id for signing")
		privKeyHex   = flag.String("privkey-hex", "", "secp256k1 private key hex for signing")
		caseID       = flag.String("case-id", "", "case id for optional signed vote")
		height       = flag.Uint64("height", 1, "observed and signed height")
		expiryHeight = flag.Uint64("expiry-height", 101, "risk report expiry height")
	)
	flag.Parse()

	if *txPath == "" {
		log.Fatal("-tx is required")
	}
	tx, err := readTx(*txPath)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	risk, err := riskclient.New(*riskURL, 5*time.Second).ScreenTx(ctx, riskclient.TxScreenRequest(tx))
	if err != nil {
		log.Fatal(err)
	}

	explanation, err := llmclient.New(*llmURL, *llmProvider, *llmModel, 10*time.Second).Explain(ctx, llmclient.ExplainRequest{
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
		log.Fatal(err)
	}

	action := policy.Action(
		risk.RiskScore,
		risk.RiskCategories,
		sanctiontypes.SanctionTargetType_SANCTION_TARGET_TYPE_TX,
		policy.DefaultThresholds(),
	)

	result := output{
		Risk:        risk,
		Explanation: explanation,
		Action:      sanctiontypes.ActionName(action),
	}

	if *agentID != "" && *privKeyHex != "" {
		s, err := signer.New(*agentID, *privKeyHex)
		if err != nil {
			log.Fatal(err)
		}
		txHash, err := hex.DecodeString(tx.TxHash)
		if err != nil {
			log.Fatalf("tx_hash must be hex when signing: %v", err)
		}
		rationaleHash := signer.RationaleHash(explanation.Rationale, explanation.Caveats)
		report := sanctiontypes.RiskReport{
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
			ObservedHeight:   *height,
			ExpiryHeight:     *expiryHeight,
			PolicyVersion:    "poc-v1",
			EvidenceHash:     signer.EvidenceHash([]byte(tx.TxHash), []byte(risk.ResponseID)),
			LlmRationaleHash: rationaleHash,
		}
		report, err = s.SignRiskReport(*chainID, report)
		if err != nil {
			log.Fatal(err)
		}
		result.RiskReport = &report
		result.AgentInfo = &sanctiontypes.AgentInfo{
			AgentId:       *agentID,
			SignerAddress: s.Address,
			PublicKey:     s.PublicKey(),
			VotingPower:   1,
			Active:        true,
		}
		if *caseID != "" && action != sanctiontypes.SanctionAction_SANCTION_ACTION_UNSPECIFIED {
			vote, err := s.SignVote(*chainID, sanctiontypes.SanctionVote{
				CaseId:         *caseID,
				Option:         sanctiontypes.VoteOption_VOTE_OPTION_APPROVE,
				ApprovedAction: action,
				ReasonCode:     "policy_match",
				RationaleHash:  rationaleHash,
				SignedHeight:   *height,
			})
			if err != nil {
				log.Fatal(err)
			}
			result.Vote = &vote
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		log.Fatal(err)
	}
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
