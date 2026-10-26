package agentconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadKeyFileRejectsSharedOrMissingKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.hex")
	if _, err := ReadKeyFile(path); err == nil {
		t.Fatal("missing key was ignored")
	}
	if err := os.WriteFile(path, []byte("  0101\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := ReadKeyFile(path)
	if err != nil || key != "0101" {
		t.Fatalf("key=%q err=%v", key, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadKeyFile(path); err == nil {
		t.Fatal("world-readable private key accepted")
	}
}

func TestLoadRPCPoolAndStrictZeroHeightLag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	err := os.WriteFile(path, []byte(`{"rpc_endpoints":["http://a:26657","http://b:26657","http://c:26657"],"rpc_safety":{"max_block_age_seconds":15,"max_future_skew_seconds":3,"max_height_lag":0,"min_agreement":2,"probe_timeout_seconds":3}}`), 0600)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || len(cfg.RPCEndpoints) != 3 || cfg.RPCSafety.MaxHeightLag == nil || *cfg.RPCSafety.MaxHeightLag != 0 || cfg.RPCSafety.MinAgreement != 2 {
		t.Fatalf("pool config not preserved: %+v, %v", cfg.RPCSafety, err)
	}
}
