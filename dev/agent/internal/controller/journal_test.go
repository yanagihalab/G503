package controller

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestControllerSIGKILLRecovery(t *testing.T) {
	if dir := os.Getenv("G503_CONTROLLER_TEST_CHILD"); dir != "" {
		j, err := Open(filepath.Join(dir, "controller.db"), "inventory-v1", false)
		if err != nil {
			os.Exit(11)
		}
		_ = j.Step(context.Background(), "send", nil, func(_ context.Context, id string) (any, error) {
			if err := os.WriteFile(filepath.Join(dir, "operation-id"), []byte(id), 0600); err != nil {
				os.Exit(12)
			}
			for {
				time.Sleep(time.Second)
			}
		})
		os.Exit(13)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "controller.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestControllerSIGKILLRecovery$")
	cmd.Env = append(os.Environ(), "G503_CONTROLLER_TEST_CHILD="+dir)
	require.NoError(t, cmd.Start())
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	var original []byte
	require.Eventually(t, func() bool { original, _ = os.ReadFile(filepath.Join(dir, "operation-id")); return len(original) == 64 }, 10*time.Second, 10*time.Millisecond)
	_, err := Open(path, "inventory-v1", true)
	require.Error(t, err, "second live controller must not steal ownership")
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	j, err := Open(path, "inventory-v1", true)
	require.NoError(t, err)
	var result string
	require.NoError(t, j.Step(context.Background(), "send", &result, func(_ context.Context, id string) (any, error) {
		require.Equal(t, string(original), id)
		return "reconciled", nil
	}))
	require.Equal(t, "reconciled", result)
	require.NoError(t, j.Step(context.Background(), "send", &result, func(context.Context, string) (any, error) { t.Fatal("completed operation replayed"); return nil, nil }))
	require.NoError(t, j.Close())
	_, err = Open(path, "changed-inventory", true)
	require.ErrorContains(t, err, "mismatch")
	_, err = Open(path, "inventory-v1", false)
	require.Error(t, err)
	j, err = Open(path, "inventory-v1", true)
	require.NoError(t, err)
	require.NoError(t, j.Finish("cleaned"))
	require.NoError(t, j.Close())
	_, err = Open(path, "inventory-v1", true)
	require.ErrorContains(t, err, "cleaned")
}
