package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type riskResponse struct {
	Source         string   `json:"source"`
	ResponseID     string   `json:"response_id"`
	Address        string   `json:"address,omitempty"`
	TxHash         string   `json:"tx_hash,omitempty"`
	RiskScore      uint32   `json:"risk_score"`
	RiskCategories []string `json:"risk_categories"`
	ExposureType   string   `json:"exposure_type"`
	UpdatedAt      string   `json:"updated_at"`
}

type txScreenRequest struct {
	TxHash    string `json:"tx_hash"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Amount    string `json:"amount"`
	Denom     string `json:"denom"`
}

func main() {
	addr := flag.String("listen", "127.0.0.1:8081", "listen address")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/address/", handleAddressRisk)
	mux.HandleFunc("/v1/tx/screen", handleTxScreen)
	log.Printf("mock risk service listening on http://%s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

func handleAddressRisk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	address := strings.TrimPrefix(r.URL.Path, "/v1/address/")
	address = strings.TrimSuffix(address, "/risk")
	if address == "" || strings.Contains(address, "/") {
		http.Error(w, "invalid address path", http.StatusBadRequest)
		return
	}
	writeJSON(w, classify(address, "", address))
}

func handleTxScreen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req txScreenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.TxHash == "" || req.Recipient == "" {
		http.Error(w, "tx_hash and recipient are required", http.StatusBadRequest)
		return
	}
	writeJSON(w, classify(req.Recipient, req.TxHash, req.Recipient))
}

func classify(key string, txHash string, address string) riskResponse {
	score := uint32(12)
	categories := []string{"low_risk"}
	exposure := "none"
	lower := strings.ToLower(key)
	switch {
	case strings.Contains(lower, "sanction"):
		score = 98
		categories = []string{"sanctioned", "money_laundering"}
		exposure = "direct"
	case strings.Contains(lower, "scam"):
		score = 86
		categories = []string{"scam"}
		exposure = "direct"
	case strings.Contains(lower, "launder"):
		score = 92
		categories = []string{"money_laundering"}
		exposure = "indirect"
	case strings.Contains(lower, "watch"):
		score = 65
		categories = []string{"unknown"}
		exposure = "indirect"
	}
	return riskResponse{
		Source:         "chainalysis-mock",
		ResponseID:     fmt.Sprintf("risk_%d", time.Now().UnixNano()),
		Address:        address,
		TxHash:         txHash,
		RiskScore:      score,
		RiskCategories: categories,
		ExposureType:   exposure,
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
