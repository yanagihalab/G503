package agentapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type Client struct {
	Endpoint   string
	Token      string
	Role       string
	HTTPClient *http.Client
}

type TLSConfig struct {
	CertFile string
	KeyFile  string
	CAFile   string
}

type EvaluateRequest struct {
	AgentID    string      `json:"agent_id"`
	TargetType string      `json:"target_type"`
	Tx         TxInput     `json:"tx"`
	Risk       RiskInput   `json:"risk"`
	Policy     PolicyInput `json:"policy"`
}

type TxInput struct {
	TxHash    string `json:"tx_hash"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Amount    string `json:"amount"`
	Denom     string `json:"denom"`
	Confirmed bool   `json:"confirmed"`
}

type RiskInput struct {
	Source       string   `json:"source"`
	ResponseID   string   `json:"response_id"`
	Address      string   `json:"address"`
	Score        uint32   `json:"score"`
	Categories   []string `json:"categories"`
	ExposureType string   `json:"exposure_type"`
}

type PolicyInput struct {
	WatchThreshold   uint32   `json:"watch_threshold"`
	BlockThreshold   uint32   `json:"block_threshold"`
	FreezeThreshold  uint32   `json:"freeze_threshold"`
	RevertThreshold  uint32   `json:"revert_threshold"`
	HighRisk         []string `json:"high_risk_categories"`
	CriticalRisk     []string `json:"critical_risk_categories,omitempty"`
	TrustedAddresses []string `json:"trusted_addresses,omitempty"`
	BlockedAddresses []string `json:"blocked_addresses,omitempty"`
}

type EvaluateResponse struct {
	AgentID          string   `json:"agent_id"`
	DecisionID       string   `json:"decision_id"`
	Approved         bool     `json:"approved"`
	VoteOption       string   `json:"vote_option"`
	Action           string   `json:"action"`
	RawRiskScore     uint32   `json:"raw_risk_score"`
	CalibratedScore  uint32   `json:"calibrated_score"`
	Confidence       float64  `json:"confidence"`
	ReviewLevel      string   `json:"review_level"`
	EvidenceStatus   string   `json:"evidence_status"`
	Rationale        string   `json:"rationale"`
	RequiredEvidence []string `json:"required_evidence"`
	Findings         []string `json:"findings"`
	Deterministic    bool     `json:"deterministic"`
	LatencyMS        int64    `json:"latency_ms"`
	CreatedAt        string   `json:"created_at"`
}

func New(endpoint string, token string, role string, timeout time.Duration, tlsConfig TLSConfig) (Client, error) {
	httpClient := &http.Client{Timeout: timeout}
	if tlsConfig.CertFile != "" || tlsConfig.KeyFile != "" || tlsConfig.CAFile != "" {
		config, err := loadTLSConfig(tlsConfig)
		if err != nil {
			return Client{}, err
		}
		httpClient.Transport = &http.Transport{TLSClientConfig: config}
	}
	return Client{
		Endpoint:   strings.TrimRight(endpoint, "/"),
		Token:      token,
		Role:       role,
		HTTPClient: httpClient,
	}, nil
}

func (c Client) Evaluate(ctx context.Context, request EvaluateRequest) (EvaluateResponse, []byte, error) {
	var response EvaluateResponse
	body, err := json.Marshal(request)
	if err != nil {
		return response, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+"/v1/agents/evaluate", bytes.NewReader(body))
	if err != nil {
		return response, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.Role != "" {
		req.Header.Set("X-Agent-Role", c.Role)
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return response, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return response, nil, fmt.Errorf("agent API returned %s", resp.Status)
	}
	var raw bytes.Buffer
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return response, nil, err
	}
	rawBytes, err := json.Marshal(response)
	if err != nil {
		return response, nil, err
	}
	raw.Write(rawBytes)
	return response, raw.Bytes(), nil
}

func loadTLSConfig(paths TLSConfig) (*tls.Config, error) {
	if paths.CertFile == "" || paths.KeyFile == "" || paths.CAFile == "" {
		return nil, fmt.Errorf("mtls cert, key, and ca files must all be provided")
	}
	cert, err := tls.LoadX509KeyPair(paths.CertFile, paths.KeyFile)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(paths.CAFile)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("failed to parse ca file %s", paths.CAFile)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		RootCAs:      roots,
	}, nil
}
