package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditBundlePreservesEvidenceAndStableHash(t *testing.T) {
	dir := t.TempDir()
	casePath, decisionPath := filepath.Join(dir, "case.json"), filepath.Join(dir, "decision.json")
	if err := os.WriteFile(casePath, []byte(`{"case_id":"c1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(decisionPath, []byte(`{"action":"watch"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var firstHash string
	for i := 0; i < 8; i++ {
		out := filepath.Join(dir, "bundle.json")
		if err := auditBundleCmd([]string{"-case", casePath, "-decision", decisionPath, "-out", out}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		var bundle struct {
			Hash     string            `json:"evidence_hash_hex"`
			Contents map[string][]byte `json:"artifact_contents"`
		}
		if err := json.Unmarshal(data, &bundle); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstHash = bundle.Hash
		}
		if bundle.Hash != firstHash {
			t.Fatal("identical artifacts produced different hashes")
		}
		if string(bundle.Contents["case"]) != `{"case_id":"c1"}` {
			t.Fatal("evidence contents were not preserved")
		}
	}
	if err := auditBundleCmd([]string{"-out", filepath.Join(dir, "empty.json")}); err == nil {
		t.Fatal("empty evidence accepted")
	}
}

func TestRotateKeyDoesNotOverwriteExistingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.hex")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rotateKeyCmd([]string{"-out", path}); err == nil {
		t.Fatal("existing private key overwritten")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original" {
		t.Fatal("existing key changed")
	}
}

func TestReviewRequiresSHA256Evidence(t *testing.T) {
	for _, hash := range []string{"", "zz", "aa"} {
		err := sanctionDecisionCmd("review-sanction", []string{"-case-id", "case-1", "-privkey-hex", strings.Repeat("01", 32), "-evidence-hash-hex", hash, "-dry-run"})
		if err == nil || !strings.Contains(err.Error(), "32-byte SHA-256") {
			t.Fatalf("malformed review evidence accepted: %q, %v", hash, err)
		}
	}
}
