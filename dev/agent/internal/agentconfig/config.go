package agentconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	ChainID       string          `json:"chain_id"`
	RPCURL        string          `json:"rpc_url"`
	RiskURL       string          `json:"risk_url"`
	RiskProvider  string          `json:"risk_provider"`
	RiskAPIKeyEnv string          `json:"risk_api_key_env"`
	LLMURL        string          `json:"llm_url"`
	LLMProvider   string          `json:"llm_provider"`
	LLMModel      string          `json:"llm_model"`
	AgentID       string          `json:"agent_id"`
	PrivateKeyHex string          `json:"private_key_hex"`
	KeyFile       string          `json:"key_file"`
	AgentAPI      AgentAPIConfig  `json:"agent_api"`
	Broadcast     BroadcastConfig `json:"broadcast"`
	Daemon        DaemonConfig    `json:"daemon"`
}

type DaemonConfig struct {
	Enabled     bool   `json:"enabled"`
	StateDir    string `json:"state_dir"`
	StartHeight uint64 `json:"start_height"`
}

type AgentAPIConfig struct {
	URL          string   `json:"url"`
	TokenEnv     string   `json:"token_env"`
	Role         string   `json:"role"`
	MTLSCertFile string   `json:"mtls_cert_file"`
	MTLSKeyFile  string   `json:"mtls_key_file"`
	MTLSCAFile   string   `json:"mtls_ca_file"`
	Trusted      []string `json:"trusted_addresses"`
	Blocked      []string `json:"blocked_addresses"`
}

type BroadcastConfig struct {
	AccountNumber uint64 `json:"account_number"`
	Sequence      uint64 `json:"sequence"`
	Gas           uint64 `json:"gas"`
	Fee           string `json:"fee"`
}

func Load(path string) (Config, error) {
	var cfg Config
	if path == "" {
		return cfg, nil
	}
	bz, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(bz, &cfg); err != nil {
		return cfg, err
	}
	if cfg.RiskAPIKeyEnv != "" && cfg.RiskAPIKeyEnv != "-" {
		cfg.RiskProvider = defaultString(cfg.RiskProvider, "chainalysis")
	}
	return cfg, nil
}

func ReadKeyFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("key file must be a regular file accessible only by its owner: %s", path)
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(key)), nil
}

func (c Config) RiskAPIKey() string {
	if c.RiskAPIKeyEnv == "" {
		return ""
	}
	return os.Getenv(c.RiskAPIKeyEnv)
}

func (c Config) AgentAPIToken() string {
	if c.AgentAPI.TokenEnv == "" {
		return ""
	}
	return os.Getenv(c.AgentAPI.TokenEnv)
}

func (c Config) ValidateForAgentAPI() error {
	if c.AgentAPI.URL == "" {
		return nil
	}
	if c.AgentID == "" {
		return fmt.Errorf("agent_id is required when agent_api.url is set")
	}
	if c.AgentAPI.TokenEnv == "" {
		return fmt.Errorf("agent_api.token_env is required when agent_api.url is set")
	}
	if os.Getenv(c.AgentAPI.TokenEnv) == "" {
		return fmt.Errorf("environment variable %s is empty", c.AgentAPI.TokenEnv)
	}
	return nil
}

func defaultString(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
