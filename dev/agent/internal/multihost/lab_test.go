package multihost

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func inventory() Inventory {
	i := Inventory{RunID: "test-run", ChainID: "g503-lab", Repository: "https://github.com/yanagihalab/G503.git", Revision: strings.Repeat("a", 40), RemoteBase: "/var/tmp/g503-lab"}
	for n := 1; n <= 5; n++ {
		i.Hosts = append(i.Hosts, Host{ID: fmt.Sprintf("node%d", n), SSH: fmt.Sprintf("user@node%d", n), Port: 22, PeerIP: fmt.Sprintf("192.0.2.%d", n)})
	}
	return i
}

func TestInventoryRejectsUnsafeOrNonDistinctHosts(t *testing.T) {
	require.NoError(t, inventory().Validate())
	for _, change := range []func(*Inventory){
		func(i *Inventory) { i.Hosts = i.Hosts[:4] },
		func(i *Inventory) { i.Hosts[1].ID = i.Hosts[0].ID },
		func(i *Inventory) { i.Hosts[1].SSH = i.Hosts[0].SSH },
		func(i *Inventory) { i.Hosts[1].PeerIP = i.Hosts[0].PeerIP },
		func(i *Inventory) { i.Hosts[0].SSH = "-oProxyCommand=evil" },
		func(i *Inventory) { i.Hosts[0].Port = 26657 },
		func(i *Inventory) { i.Hosts[0].PeerIP = "not-an-ip" },
		func(i *Inventory) { i.RemoteBase = "/" },
		func(i *Inventory) { i.RemoteBase = "/var/tmp/../home" },
		func(i *Inventory) { i.RunID = "x; rm -rf /" },
		func(i *Inventory) { i.Revision = "main" },
		func(i *Inventory) { i.Revision = strings.Repeat("0", 40) },
		func(i *Inventory) { i.Repository = "https://secret@github.com/repo" },
		func(i *Inventory) { i.Repository += "?token=secret" },
	} {
		i := inventory()
		change(&i)
		require.Error(t, i.Validate())
	}
	value := "spaces ' \" ; $(exit 42)\nline"
	bz, err := exec.Command("bash", "-c", "printf '%s' "+Quote(value)).Output()
	require.NoError(t, err)
	require.Equal(t, value, string(bz))
}

func freshGenesis(n int) []byte {
	user, module := fmt.Sprintf("user%d", n), "module"
	doc := map[string]any{
		"validators": []any{map[string]any{"address": fmt.Sprintf("val%d", n)}},
		"app_state": map[string]any{
			"auth":     map[string]any{"accounts": []any{map[string]any{"address": user}, map[string]any{"base_account": map[string]any{"address": module}}}},
			"bank":     map[string]any{"balances": []any{map[string]any{"address": user, "coins": []any{map[string]string{"denom": "stake", "amount": "1000000000"}}}}},
			"sanction": map[string]any{"agents": []any{map[string]string{"agent_id": "default"}}, "params": map[string]any{}},
		},
	}
	bz, _ := json.Marshal(doc)
	return bz
}

func TestMergeGenesisDeduplicatesModulesAndPreservesSupply(t *testing.T) {
	documents := make([][]byte, 5)
	for n := range documents {
		documents[n] = freshGenesis(n)
	}
	merged, err := mergeGenesis(documents)
	require.NoError(t, err)
	require.Len(t, merged["validators"], 5)
	app := merged["app_state"].(map[string]any)
	require.Len(t, app["auth"].(map[string]any)["accounts"], 6)
	require.Len(t, app["sanction"].(map[string]any)["agents"], 5)
	require.Equal(t, "5000000000", app["bank"].(map[string]any)["supply"].([]any)[0].(map[string]any)["amount"])
	_, err = mergeGenesis([][]byte{[]byte(`{"validators":[{}],"app_state":{}}`)})
	require.ErrorContains(t, err, "invalid fresh genesis")
}

type fakeTransport struct {
	phase              string
	stateReads         int
	scripts            []string
	duplicateMachineID bool
	failProvision      bool
	failCleanup        bool
	interruptAction    string
}

func (f *fakeTransport) Run(_ context.Context, h Host, script string) ([]byte, error) {
	f.scripts = append(f.scripts, script)
	if f.interruptAction != "" && strings.Contains(script, "'"+f.interruptAction+"'") {
		f.interruptAction = ""
		return nil, fmt.Errorf("simulated controller connection interruption")
	}
	if strings.Contains(script, "cat /etc/machine-id") {
		if f.duplicateMachineID {
			return []byte("same-machine"), nil
		}
		return []byte(h.ID), nil
	}
	if strings.Contains(script, "git clone") && f.failProvision {
		return nil, fmt.Errorf("provision failure")
	}
	if strings.Contains(script, " cleanup; fi") && f.failCleanup {
		return nil, fmt.Errorf("cleanup failure")
	}
	if strings.HasSuffix(script, "'genesis'\n") {
		return freshGenesis(int(h.ID[4] - '0')), nil
	}
	if strings.HasSuffix(script, "'node-id'\n") {
		return []byte(fmt.Sprintf("%040d", int(h.ID[4]-'0'))), nil
	}
	if strings.Contains(script, "'transfer'") {
		return []byte(`{"tx_hash":"` + strings.Repeat("A", 64) + `"}`), nil
	}
	if strings.Contains(script, "'block-hash'") {
		return []byte(strings.Repeat("B", 64)), nil
	}
	if strings.HasSuffix(script, "'state'\n") {
		f.stateReads++
		height := 100
		switch f.phase {
		case "4-1-partition":
			if f.stateReads > 5 && h.ID != "node5" {
				height++
			}
		case "3-2-halt-and-stale-send-suppression":
			height = 200
			if f.stateReads > 10 {
				height++
			}
		case "3-2-resumed":
			height = 201
		}
		return []byte(fmt.Sprintf(`{"status":{"result":{"sync_info":{"latest_block_height":"%d"}}},"sends":10}`, height)), nil
	}
	return []byte("logs-or-success"), nil
}

func testRunner(t *testing.T, transport *fakeTransport) *Runner {
	r := &Runner{Inventory: inventory(), Transport: transport, ResultDir: filepath.Join(t.TempDir(), "results"), sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }}
	r.Progress = func(phase string) { transport.phase, transport.stateReads = phase, 0 }
	return r
}

func TestPlanNeverAccessesHosts(t *testing.T) {
	f := &fakeTransport{}
	summary, err := testRunner(t, f).Plan()
	require.NoError(t, err)
	require.Equal(t, "planned", summary.Status)
	require.False(t, summary.RealHostsExecuted)
	require.Empty(t, f.scripts)
}

func TestFiveHostControllerFaultFlowAndCleanup(t *testing.T) {
	f := &fakeTransport{}
	r := testRunner(t, f)
	summary, err := r.Execute(context.Background())
	require.NoError(t, err)
	require.Equal(t, "passed", summary.Status)
	require.True(t, summary.CleanupSucceeded)
	require.False(t, summary.RealHostsExecuted, "simulated SSH must never be reported as real hosts")
	scripts := strings.Join(f.scripts, "\n")
	require.Equal(t, 10, strings.Count(scripts, "'partition'"))
	require.Equal(t, 5, strings.Count(scripts, "'block-hash'"))
	require.Equal(t, 5, strings.Count(scripts, " cleanup; fi"))
	require.Equal(t, 5, strings.Count(scripts, "'collect'"))
	require.NotContains(t, scripts, "StrictHostKeyChecking=no")
	require.Contains(t, scripts, `test "$(cat "$root/.g503-lab-owned"`)
	require.NotEqual(t, r.Inventory.RunID, r.owner, "each controller must own a new random marker")
	var saved Summary
	bz, err := os.ReadFile(filepath.Join(r.ResultDir, "summary.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(bz, &saved))
	require.Equal(t, summary, saved)
	_, err = r.Execute(context.Background())
	require.Error(t, err, "must not overwrite existing local evidence")
}

func TestControllerFailuresNeverReportSuccess(t *testing.T) {
	for _, f := range []*fakeTransport{{duplicateMachineID: true}, {failProvision: true}, {failCleanup: true}} {
		r := testRunner(t, f)
		summary, err := r.Execute(context.Background())
		require.Error(t, err)
		require.Equal(t, "interrupted", summary.Status)
		require.NotEmpty(t, summary.Error)
		if f.duplicateMachineID {
			require.NotContains(t, strings.Join(f.scripts, "\n"), "git clone")
		}
		if f.failCleanup {
			require.False(t, summary.CleanupSucceeded)
		}
	}
}

func TestControllerResumeAndExplicitCleanup(t *testing.T) {
	f := &fakeTransport{interruptAction: "stop-node"}
	r := testRunner(t, f)
	summary, err := r.Execute(context.Background())
	require.Error(t, err)
	require.Equal(t, "interrupted", summary.Status)
	owner := r.owner
	before := strings.Count(strings.Join(f.scripts, "\n"), "'transfer'")
	r.Resume = true
	summary, err = r.Execute(context.Background())
	require.NoError(t, err)
	require.Equal(t, "passed", summary.Status)
	require.Equal(t, owner, r.owner)
	require.Equal(t, before+1, strings.Count(strings.Join(f.scripts, "\n"), "'transfer'"), "completed transfers must be skipped on resume")
	r.CleanupOnly = true
	summary, err = r.Execute(context.Background())
	require.NoError(t, err)
	require.Equal(t, "cleaned", summary.Status)
	r.CleanupOnly = false
	_, err = r.Execute(context.Background())
	require.ErrorContains(t, err, "cleaned")
}

func TestWorkerRejectsOldControllerBeforeAnyAction(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".g503-lab-owned"), []byte("current-owner"), 0600))
	worker := filepath.Join("..", "..", "..", "scripts", "multihost-worker.sh")
	_, err := os.Stat(worker)
	require.NoError(t, err)
	for _, action := range []string{"cleanup", "partition", "collect"} {
		cmd := exec.Command("bash", worker, root, "test-run", "old-owner", action)
		require.Error(t, cmd.Run())
	}
}

func TestWorkerScopesFirewallAndCollectsOnlyLogs(t *testing.T) {
	root, helpers := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".g503-lab-owned"), []byte("owner"), 0600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "logs"), 0700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "pids"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "logs", "node.log"), []byte("public log"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "secret.json"), []byte("NEVER COLLECT THIS"), 0600))
	logPath := filepath.Join(root, "firewall.log")
	require.NoError(t, os.WriteFile(filepath.Join(helpers, "sudo"), []byte("#!/bin/bash\nprintf '%s\\n' \"$*\" >>\"$FAKE_LOG\"\n"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(helpers, "flock"), []byte("#!/bin/bash\nexit 0\n"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(helpers, "sha256sum"), []byte("#!/bin/bash\ncat >/dev/null\nprintf '%064d  -\\n' 1\n"), 0700))
	worker := filepath.Join("..", "..", "..", "scripts", "multihost-worker.sh")
	run := func(action string, args ...string) []byte {
		cmd := exec.Command("bash", append([]string{worker, root, "test-run", "owner", action}, args...)...)
		cmd.Env = append(os.Environ(), "PATH="+helpers+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_LOG="+logPath)
		bz, err := cmd.CombinedOutput()
		require.NoError(t, err, string(bz))
		return bz
	}
	run("partition", "192.0.2.5")
	run("cleanup")
	bz, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Contains(t, string(bz), "iptables -F G503_000000000000\n")
	require.NotContains(t, string(bz), "iptables -F\n")
	require.Contains(t, string(bz), "--ports 26656,26657,1317,8081,8082,8083 -j DROP")
	require.NotContains(t, string(bz), "--ports 22")
	_, err = os.Stat(filepath.Join(root, "firewall.owner"))
	require.True(t, os.IsNotExist(err))
	archive := run("collect")
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	require.NoError(t, err)
	defer gz.Close()
	tr := tar.NewReader(gz)
	var entries []string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		entries = append(entries, header.Name)
		require.True(t, header.Name == "logs" || strings.HasPrefix(header.Name, "logs/"), header.Name)
	}
	require.Contains(t, entries, "logs/node.log")
}
