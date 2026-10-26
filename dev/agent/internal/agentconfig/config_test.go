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
