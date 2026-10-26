package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type evaluateRequest struct {
	AgentID    string      `json:"agent_id"`
	TargetType string      `json:"target_type"`
	Tx         txInput     `json:"tx"`
	Risk       riskInput   `json:"risk"`
	Policy     policyInput `json:"policy"`
}

type txInput struct {
	TxHash    string `json:"tx_hash"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Amount    string `json:"amount"`
	Denom     string `json:"denom"`
	Confirmed bool   `json:"confirmed"`
}

type riskInput struct {
	Source       string   `json:"source"`
	ResponseID   string   `json:"response_id"`
	Address      string   `json:"address"`
	Score        uint32   `json:"score"`
	Categories   []string `json:"categories"`
	ExposureType string   `json:"exposure_type"`
}

type policyInput struct {
	WatchThreshold   uint32   `json:"watch_threshold"`
	BlockThreshold   uint32   `json:"block_threshold"`
	FreezeThreshold  uint32   `json:"freeze_threshold"`
	RevertThreshold  uint32   `json:"revert_threshold"`
	HighRisk         []string `json:"high_risk_categories"`
	CriticalRisk     []string `json:"critical_risk_categories"`
	TrustedAddresses []string `json:"trusted_addresses"`
	BlockedAddresses []string `json:"blocked_addresses"`
}

type evaluateResponse struct {
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

func main() {
	addr := flag.String("listen", "127.0.0.1:8083", "listen address")
	latency := flag.Duration("latency", 0, "artificial processing latency")
	apiToken := flag.String("api-token", "", "optional bearer token required for API requests")
	allowedRoles := flag.String("allowed-roles", "validator-agent,operator", "comma-separated accepted X-Agent-Role values; empty disables role checks")
	rateLimit := flag.Int("rate-limit-per-minute", 120, "maximum evaluate requests per minute; 0 disables rate limiting")
	maxBodyBytes := flag.Int64("max-body-bytes", 1048576, "maximum JSON request body size")
	tlsCertFile := flag.String("tls-cert-file", "", "optional server TLS certificate file")
	tlsKeyFile := flag.String("tls-key-file", "", "optional server TLS key file")
	clientCAFile := flag.String("client-ca-file", "", "optional client CA file; enables and requires mTLS")
	flag.Parse()
	if *clientCAFile != "" && (*tlsCertFile == "" || *tlsKeyFile == "") {
		log.Fatal("client-ca-file requires a TLS server certificate and key")
	}

	handler := newAPIHandler(*apiToken, parseSet(*allowedRoles), *rateLimit, *maxBodyBytes, *latency)
	log.Printf("mock agent API listening on %s", *addr)
	server := &http.Server{Addr: *addr, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	if *clientCAFile != "" {
		tlsConfig, err := serverTLSConfig(*clientCAFile)
		if err != nil {
			log.Fatal(err)
		}
		server.TLSConfig = tlsConfig
	}
	var err error
	if *tlsCertFile != "" || *tlsKeyFile != "" {
		if *tlsCertFile == "" || *tlsKeyFile == "" {
			log.Fatal("tls-cert-file and tls-key-file must be provided together")
		}
		err = server.ListenAndServeTLS(*tlsCertFile, *tlsKeyFile)
	} else {
		err = server.ListenAndServe()
	}
	if err != nil {
		log.Fatal(err)
	}
}

func newAPIHandler(token string, roles map[string]bool, rateLimit int, maxBodyBytes int64, latency time.Duration) http.Handler {
	limiter := newRateLimiter(rateLimit)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/v1/agents/evaluate", func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if len(roles) > 0 && !roles[strings.ToLower(strings.TrimSpace(r.Header.Get("X-Agent-Role")))] {
			http.Error(w, "forbidden role", http.StatusForbidden)
			return
		}
		if !limiter.allow(r.RemoteAddr) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		handleEvaluate(w, r, latency)
	})
	return mux
}

type rateLimiter struct {
	limit   int
	mu      sync.Mutex
	window  time.Time
	clients map[string]int
}

func newRateLimiter(limit int) *rateLimiter {
	return &rateLimiter{
		limit:   limit,
		window:  time.Now(),
		clients: map[string]int{},
	}
}

func (r *rateLimiter) allow(remote string) bool {
	if r.limit <= 0 {
		return true
	}
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.window) >= time.Minute {
		r.window = time.Now()
		r.clients = map[string]int{}
	}
	r.clients[remote]++
	return r.clients[remote] <= r.limit
}

func handleEvaluate(w http.ResponseWriter, r *http.Request, artificialLatency time.Duration) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	start := time.Now()
	if artificialLatency > 0 {
		time.Sleep(artificialLatency)
	}
	var req evaluateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		status := http.StatusBadRequest
		if _, tooLarge := err.(*http.MaxBytesError); tooLarge {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, err.Error(), status)
		return
	}
	if req.AgentID == "" || req.Tx.TxHash == "" {
		http.Error(w, "agent_id and tx.tx_hash are required", http.StatusBadRequest)
		return
	}
	applyDefaults(&req)
	if req.Risk.Score > 100 || (req.TargetType != "tx" && req.TargetType != "address") ||
		req.Policy.WatchThreshold > req.Policy.BlockThreshold || req.Policy.BlockThreshold > req.Policy.FreezeThreshold ||
		req.Policy.FreezeThreshold > req.Policy.RevertThreshold || req.Policy.RevertThreshold > 100 {
		http.Error(w, "invalid score, target type, or policy thresholds", http.StatusBadRequest)
		return
	}
	decision := evaluate(req)
	action := decision.Action
	approved := action != "none"
	vote := "reject"
	if approved {
		vote = "approve"
	}
	resp := evaluateResponse{
		AgentID:          req.AgentID,
		DecisionID:       decisionID(req),
		Approved:         approved,
		VoteOption:       vote,
		Action:           action,
		RawRiskScore:     req.Risk.Score,
		CalibratedScore:  decision.Score,
		Confidence:       decision.Confidence,
		ReviewLevel:      decision.ReviewLevel,
		EvidenceStatus:   decision.EvidenceStatus,
		Rationale:        decision.Rationale,
		RequiredEvidence: decision.RequiredEvidence,
		Findings:         decision.Findings,
		Deterministic:    true,
		LatencyMS:        time.Since(start).Milliseconds(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
	}
	writeJSON(w, resp)
}

func applyDefaults(req *evaluateRequest) {
	if req.TargetType == "" {
		req.TargetType = "tx"
	}
	if req.Policy.WatchThreshold == 0 {
		req.Policy.WatchThreshold = 60
	}
	if req.Policy.BlockThreshold == 0 {
		req.Policy.BlockThreshold = 80
	}
	if req.Policy.FreezeThreshold == 0 {
		req.Policy.FreezeThreshold = 85
	}
	if req.Policy.RevertThreshold == 0 {
		req.Policy.RevertThreshold = 90
	}
	if len(req.Policy.HighRisk) == 0 {
		req.Policy.HighRisk = []string{"sanctioned", "scam", "darknet", "money_laundering", "terrorist_financing"}
	}
	if len(req.Policy.CriticalRisk) == 0 {
		req.Policy.CriticalRisk = []string{"sanctioned", "terrorist_financing", "ransomware"}
	}
	req.Policy.HighRisk = normalizeList(req.Policy.HighRisk)
	req.Policy.CriticalRisk = normalizeList(req.Policy.CriticalRisk)
	req.Policy.TrustedAddresses = normalizeList(req.Policy.TrustedAddresses)
	req.Policy.BlockedAddresses = normalizeList(req.Policy.BlockedAddresses)
	req.Risk.Categories = normalizeList(req.Risk.Categories)
	req.Risk.ExposureType = strings.ToLower(strings.TrimSpace(req.Risk.ExposureType))
}

type decisionResult struct {
	Action           string
	Score            uint32
	Confidence       float64
	ReviewLevel      string
	EvidenceStatus   string
	Rationale        string
	RequiredEvidence []string
	Findings         []string
}

func evaluate(req evaluateRequest) decisionResult {
	score, findings := calibrateRiskScore(req)
	evidenceStatus, missing := evidenceStatus(req)
	action := decideAction(req, score, evidenceStatus)
	confidence := confidence(score, findings, missing)
	return decisionResult{
		Action:           action,
		Score:            score,
		Confidence:       confidence,
		ReviewLevel:      reviewLevel(score, action, evidenceStatus),
		EvidenceStatus:   evidenceStatus,
		Rationale:        rationale(req, action, score, findings, missing),
		RequiredEvidence: requiredEvidence(missing),
		Findings:         findings,
	}
}

func calibrateRiskScore(req evaluateRequest) (uint32, []string) {
	score := int(req.Risk.Score)
	findings := []string{fmt.Sprintf("base_risk_score=%d", req.Risk.Score)}
	categoryWeights := map[string]int{
		"sanctioned":            18,
		"terrorist_financing":   18,
		"ransomware":            16,
		"money_laundering":      12,
		"scam":                  10,
		"darknet":               9,
		"mixer":                 8,
		"fraud":                 8,
		"unknown":               0,
		"low_risk":              -10,
		"false_positive_review": -20,
	}
	for _, category := range req.Risk.Categories {
		if weight, found := categoryWeights[category]; found && weight != 0 {
			score += weight
			findings = append(findings, fmt.Sprintf("category:%s:%+d", category, weight))
		}
	}
	switch req.Risk.ExposureType {
	case "direct":
		score += 8
		findings = append(findings, "exposure:direct:+8")
	case "indirect":
		score += 3
		findings = append(findings, "exposure:indirect:+3")
	case "", "none":
		score -= 3
		findings = append(findings, "exposure:none:-3")
	default:
		findings = append(findings, "exposure:"+req.Risk.ExposureType+":+0")
	}
	if amount, ok := parseAmount(req.Tx.Amount); ok {
		switch {
		case amount >= 1000000:
			score += 8
			findings = append(findings, "amount:very_large:+8")
		case amount >= 10000:
			score += 4
			findings = append(findings, "amount:large:+4")
		case amount > 0 && amount < 1:
			score -= 2
			findings = append(findings, "amount:dust:-2")
		}
	} else if req.Tx.Amount == "" {
		score -= 3
		findings = append(findings, "amount:missing:-3")
	} else {
		score -= 2
		findings = append(findings, "amount:unparseable:-2")
	}
	if stringInSlice(strings.ToLower(req.Tx.Sender), req.Policy.TrustedAddresses) ||
		stringInSlice(strings.ToLower(req.Tx.Recipient), req.Policy.TrustedAddresses) {
		score -= 25
		findings = append(findings, "policy:trusted_address:-25")
	}
	if stringInSlice(strings.ToLower(req.Tx.Sender), req.Policy.BlockedAddresses) ||
		stringInSlice(strings.ToLower(req.Tx.Recipient), req.Policy.BlockedAddresses) {
		score += 30
		findings = append(findings, "policy:blocked_address:+30")
	}
	_, missing := evidenceStatus(req)
	for _, item := range missing {
		switch item {
		case "risk_response_id", "risk_source":
			score -= 6
			findings = append(findings, "evidence:"+item+":-6")
		case "tx_hash", "recipient":
			score -= 15
			findings = append(findings, "evidence:"+item+":-15")
		default:
			score -= 3
			findings = append(findings, "evidence:"+item+":-3")
		}
	}
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	sort.Strings(findings[1:])
	return uint32(score), findings
}

func decideAction(req evaluateRequest, score uint32, evidenceStatus string) string {
	highRisk := false
	for _, category := range req.Risk.Categories {
		if stringInSlice(category, req.Policy.HighRisk) {
			highRisk = true
		}
	}
	if evidenceStatus == "insufficient" {
		if score >= req.Policy.WatchThreshold {
			return "watch"
		}
		return "none"
	}
	if !highRisk {
		if score >= req.Policy.WatchThreshold {
			return "watch"
		}
		return "none"
	}
	action := "none"
	switch {
	case score >= req.Policy.RevertThreshold:
		action = "revert_transfer"
	case score >= req.Policy.FreezeThreshold:
		action = "freeze_address"
	case score >= req.Policy.BlockThreshold:
		action = "block_tx"
	case score >= req.Policy.WatchThreshold:
		action = "watch"
	}
	if req.TargetType == "tx" && (action == "revert_transfer" || action == "freeze_address") {
		return "block_tx"
	}
	return action
}

func evidenceStatus(req evaluateRequest) (string, []string) {
	missing := []string{}
	if req.Tx.TxHash == "" {
		missing = append(missing, "tx_hash")
	}
	if req.Tx.Recipient == "" {
		missing = append(missing, "recipient")
	}
	if req.Risk.Source == "" {
		missing = append(missing, "risk_source")
	}
	if req.Risk.ResponseID == "" {
		missing = append(missing, "risk_response_id")
	}
	if len(req.Risk.Categories) == 0 {
		missing = append(missing, "risk_categories")
	}
	if len(missing) == 0 {
		return "complete", missing
	}
	if len(missing) <= 2 {
		return "partial", missing
	}
	return "insufficient", missing
}

func confidence(score uint32, findings []string, missing []string) float64 {
	value := float64(score) / 100
	value += float64(len(findings)-1) * 0.015
	value -= float64(len(missing)) * 0.08
	if value < 0.05 {
		return 0.05
	}
	if value > 0.99 {
		return 0.99
	}
	return value
}

func reviewLevel(score uint32, action string, evidenceStatus string) string {
	if evidenceStatus == "insufficient" {
		return "manual_review_required"
	}
	switch {
	case action == "none":
		return "auto_reject"
	case score >= 90:
		return "critical"
	case score >= 80:
		return "high"
	case score >= 60:
		return "monitor"
	default:
		return "manual_review_required"
	}
}

func rationale(req evaluateRequest, action string, score uint32, findings []string, missing []string) string {
	if action == "none" {
		return fmt.Sprintf("calibrated_score=%d does not exceed policy thresholds for a high-confidence sanction", score)
	}
	detail := strings.Join(findings, ";")
	if len(missing) > 0 {
		detail += ";missing=" + strings.Join(missing, ",")
	}
	return fmt.Sprintf("mechanical policy approved %s because calibrated_score=%d categories=%s exposure=%s target_type=%s findings=%s", action, score, strings.Join(req.Risk.Categories, ","), req.Risk.ExposureType, req.TargetType, detail)
}

func requiredEvidence(missing []string) []string {
	required := []string{"risk_response", "tx_metadata", "policy_thresholds"}
	for _, item := range missing {
		required = append(required, "missing:"+item)
	}
	return required
}

func decisionID(req evaluateRequest) string {
	body, _ := json.Marshal(req)
	sum := sha256.Sum256(append([]byte("g503-mechanical-v2\n"), body...))
	return "decision-" + hex.EncodeToString(sum[:])
}

func stringInSlice(value string, values []string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func normalizeList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func parseAmount(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	amount, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false
	}
	return amount, true
}

func parseSet(csv string) map[string]bool {
	out := map[string]bool{}
	for _, item := range strings.Split(csv, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		out[item] = true
	}
	return out
}

func serverTLSConfig(clientCAFile string) (*tls.Config, error) {
	caPEM, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("failed to parse client CA file %s", clientCAFile)
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ClientCAs:  pool,
		ClientAuth: tls.RequireAndVerifyClientCert,
	}, nil
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
