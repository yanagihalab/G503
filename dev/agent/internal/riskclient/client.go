package riskclient

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
	HTTPClient *http.Client
}

type TxScreenRequest struct {
	TxHash    string `json:"tx_hash"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Amount    string `json:"amount"`
	Denom     string `json:"denom"`
}

type RiskResponse struct {
	Source         string   `json:"source"`
	ResponseID     string   `json:"response_id"`
	Address        string   `json:"address,omitempty"`
	TxHash         string   `json:"tx_hash,omitempty"`
	RiskScore      uint32   `json:"risk_score"`
	RiskCategories []string `json:"risk_categories"`
	ExposureType   string   `json:"exposure_type"`
	UpdatedAt      string   `json:"updated_at"`
}

func New(endpoint string, timeout time.Duration) Client {
	return Client{
		Endpoint: strings.TrimRight(endpoint, "/"),
		HTTPClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c Client) AddressRisk(ctx context.Context, address string) (RiskResponse, error) {
	var response RiskResponse
	if address == "" {
		return response, fmt.Errorf("address is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Endpoint+"/v1/address/"+address+"/risk", nil)
	if err != nil {
		return response, err
	}
	return c.do(req)
}

func (c Client) ScreenTx(ctx context.Context, screen TxScreenRequest) (RiskResponse, error) {
	var response RiskResponse
	body, err := json.Marshal(screen)
	if err != nil {
		return response, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+"/v1/tx/screen", bytes.NewReader(body))
	if err != nil {
		return response, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

func (c Client) do(req *http.Request) (RiskResponse, error) {
	var response RiskResponse
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
		return response, fmt.Errorf("risk service returned %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return response, err
	}
	return response, nil
}
