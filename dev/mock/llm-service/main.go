package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
)

type explainRequest struct {
	Task          string            `json:"task"`
	PolicyVersion string            `json:"policy_version"`
	Tx            map[string]string `json:"tx"`
	Risk          struct {
		Source       string   `json:"source"`
		Score        uint32   `json:"score"`
		Categories   []string `json:"categories"`
		ExposureType string   `json:"exposure_type"`
	} `json:"risk"`
}

type explainResponse struct {
	RecommendedAction string   `json:"recommended_action"`
	Rationale         string   `json:"rationale"`
	Caveats           string   `json:"caveats"`
	RequiredEvidence  []string `json:"required_evidence"`
}

func main() {
	addr := flag.String("listen", "127.0.0.1:8082", "listen address")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/explain", handleExplain)
	log.Printf("mock llm service listening on http://%s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

func handleExplain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req explainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	action := "none"
	switch {
	case req.Risk.Score >= 90:
		action = "revert"
	case req.Risk.Score >= 85:
		action = "freeze"
	case req.Risk.Score >= 80:
		action = "block"
	case req.Risk.Score >= 60:
		action = "watch"
	}
	writeJSON(w, explainResponse{
		RecommendedAction: action,
		Rationale:         "Risk score and category exceed the configured policy threshold.",
		Caveats:           "This is a local mock explanation and should be replaced by the central local LLM in deployment.",
		RequiredEvidence:  []string{"risk_response", "tx_metadata", "policy_version"},
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
