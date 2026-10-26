package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

type agentOpsConfig struct {
	AgentID        string `json:"agent_id"`
	RPCURL         string `json:"rpc_url"`
	RiskURL        string `json:"risk_url"`
	LLMURL         string `json:"llm_url"`
	AgentAPIURL    string `json:"agent_api_url"`
	KeyFile        string `json:"key_file"`
	MTLSCertFile   string `json:"mtls_cert_file,omitempty"`
	MTLSKeyFile    string `json:"mtls_key_file,omitempty"`
	MTLSCAFile     string `json:"mtls_ca_file,omitempty"`
	APITokenEnvVar string `json:"api_token_env_var,omitempty"`
}

func opsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ops",
		Short: "Operational helpers for G503 sanction deployments",
	}
	cmd.AddCommand(opsConfigTemplateCmd())
	cmd.AddCommand(opsValidateConfigCmd())
	cmd.AddCommand(opsAuditBundleCmd())
	return cmd
}

func opsConfigTemplateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config-template",
		Short: "Print an agent operations config template",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(agentOpsConfig{
				AgentID:        "validator-1",
				RPCURL:         "http://127.0.0.1:26657",
				RiskURL:        "https://api.chainalysis.com",
				LLMURL:         "http://127.0.0.1:11434",
				AgentAPIURL:    "http://127.0.0.1:8083",
				KeyFile:        "/etc/g503/agent-key.hex",
				MTLSCertFile:   "/etc/g503/tls/client.crt",
				MTLSKeyFile:    "/etc/g503/tls/client.key",
				MTLSCAFile:     "/etc/g503/tls/ca.crt",
				APITokenEnvVar: "G503_AGENT_API_TOKEN",
			})
		},
	}
}

func opsValidateConfigCmd() *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "validate-config",
		Short: "Validate an agent operations config file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			bz, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var cfg agentOpsConfig
			if err := json.Unmarshal(bz, &cfg); err != nil {
				return err
			}
			if cfg.AgentID == "" || cfg.RPCURL == "" || cfg.RiskURL == "" || cfg.LLMURL == "" || cfg.KeyFile == "" {
				return fmt.Errorf("agent_id, rpc_url, risk_url, llm_url, and key_file are required")
			}
			if cfg.APITokenEnvVar == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "warning: api_token_env_var is empty")
			}
			if cfg.MTLSCertFile == "" || cfg.MTLSKeyFile == "" || cfg.MTLSCAFile == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "warning: mTLS files are not fully configured")
			}
			fmt.Fprintln(cmd.OutOrStdout(), "config ok")
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "config", "", "path to operations config JSON")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

func opsAuditBundleCmd() *cobra.Command {
	var out string
	var caseFile string
	var votesFile string
	var executionFile string
	var reason string
	cmd := &cobra.Command{
		Use:   "audit-bundle",
		Short: "Create a false-positive review audit bundle with a tamper-evident hash",
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle := map[string]any{
				"created_at": time.Now().UTC().Format(time.RFC3339),
				"reason":     reason,
				"artifacts":  map[string]string{},
			}
			hash := sha256.New()
			for name, path := range map[string]string{"case": caseFile, "votes": votesFile, "execution": executionFile} {
				if path == "" {
					continue
				}
				bz, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				hash.Write([]byte(name))
				hash.Write(bz)
				bundle["artifacts"].(map[string]string)[name] = path
			}
			bundle["evidence_hash_hex"] = hex.EncodeToString(hash.Sum(nil))
			bz, err := json.MarshalIndent(bundle, "", "  ")
			if err != nil {
				return err
			}
			if out == "" {
				_, err = cmd.OutOrStdout().Write(append(bz, '\n'))
				return err
			}
			return os.WriteFile(out, append(bz, '\n'), 0o600)
		},
	}
	cmd.Flags().StringVar(&caseFile, "case", "", "case query JSON file")
	cmd.Flags().StringVar(&votesFile, "votes", "", "votes query JSON file")
	cmd.Flags().StringVar(&executionFile, "execution", "", "execution query JSON file")
	cmd.Flags().StringVar(&reason, "reason", "false-positive-review", "review reason")
	cmd.Flags().StringVar(&out, "out", "", "output audit bundle path")
	return cmd
}
