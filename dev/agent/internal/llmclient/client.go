package llmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	Endpoint   string
	Provider   string
	Model      string
	HTTPClient *http.Client
}

type ExplainRequest struct {
	Task          string            `json:"task"`
	PolicyVersion string            `json:"policy_version"`
	Tx            map[string]string `json:"tx"`
	Risk          RiskInput         `json:"risk"`
}

type RiskInput struct {
	Source       string   `json:"source"`
	Score        uint32   `json:"score"`
	Categories   []string `json:"categories"`
	ExposureType string   `json:"exposure_type"`
}

type ExplainResponse struct {
	RecommendedAction string   `json:"recommended_action"`
	Rationale         string   `json:"rationale"`
	Caveats           string   `json:"caveats"`
	RequiredEvidence  []string `json:"required_evidence"`
}

func New(endpoint string, provider string, model string, timeout time.Duration) Client {
	return Client{
		Endpoint: strings.TrimRight(endpoint, "/"),
		Provider: provider,
		Model:    model,
		HTTPClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c Client) Explain(ctx context.Context, request ExplainRequest) (ExplainResponse, error) {
	switch c.Provider {
	case "", "mock":
		return c.explainMock(ctx, request)
	case "ollama":
		return c.explainOllama(ctx, request)
	default:
		return ExplainResponse{}, fmt.Errorf("unsupported llm provider: %s", c.Provider)
	}
}

func (c Client) explainMock(ctx context.Context, request ExplainRequest) (ExplainResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return ExplainResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+"/v1/explain", bytes.NewReader(body))
	if err != nil {
		return ExplainResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.doExplain(req)
}

func (c Client) explainOllama(ctx context.Context, request ExplainRequest) (ExplainResponse, error) {
	promptBytes, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return ExplainResponse{}, err
	}
	payload := map[string]any{
		"model":  c.Model,
		"stream": false,
		"format": "json",
		"prompt": "Return JSON with recommended_action, rationale, caveats, and required_evidence for this risk review.\n" + string(promptBytes),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ExplainResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return ExplainResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return ExplainResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ExplainResponse{}, fmt.Errorf("ollama returned %s", resp.Status)
	}
	var ollama struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ollama); err != nil {
		return ExplainResponse{}, err
	}
	var explanation ExplainResponse
	if err := json.Unmarshal([]byte(ollama.Response), &explanation); err != nil {
		return ExplainResponse{}, err
	}
	return explanation, nil
}

func (c Client) doExplain(req *http.Request) (ExplainResponse, error) {
	var response ExplainResponse
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return response, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return response, fmt.Errorf("llm service returned %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return response, err
	}
	return response, nil
}
