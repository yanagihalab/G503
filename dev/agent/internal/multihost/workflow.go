package multihost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yanagihalab/G503/dev/agent/internal/controller"
)

type bootstrap struct {
	Genesis [][]byte
	NodeIDs []string
}

func (r *Runner) Execute(ctx context.Context) (summary Summary, retErr error) {
	if _, err := r.Plan(); err != nil {
		return summary, err
	}
	if r.Transport == nil {
		return summary, fmt.Errorf("transport is required")
	}
	if !r.Resume {
		if r.CleanupOnly {
			return summary, fmt.Errorf("cleanup requires explicit resume")
		}
		if err := os.Mkdir(r.ResultDir, 0700); err != nil {
			return summary, err
		}
	}
	info, err := os.Lstat(r.ResultDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return summary, fmt.Errorf("results must be a private directory, not a symlink")
	}
	j, err := controller.Open(filepath.Join(r.ResultDir, "controller.db"), struct {
		Version   int
		Inventory Inventory
	}{1, r.Inventory}, r.Resume)
	if err != nil {
		return summary, err
	}
	defer j.Close()
	r.journal, r.owner = j, j.Owner
	_, real := r.Transport.(SSHTransport)
	summary = Summary{Status: "interrupted", Revision: r.Inventory.Revision, RealHostsExecuted: real}
	defer func() {
		if retErr != nil {
			summary.Error = retErr.Error()
		}
		bz, err := json.MarshalIndent(summary, "", "  ")
		if err == nil {
			err = atomicResult(filepath.Join(r.ResultDir, "summary.json"), bz)
		}
		retErr = errors.Join(retErr, err)
	}()
	if r.CleanupOnly {
		if err := r.cleanup(ctx); err != nil {
			return summary, err
		}
		summary.Status, summary.CleanupSucceeded = "cleaned", true
		return summary, j.Finish("cleaned")
	}
	if err := r.workflow(ctx, &summary); err != nil {
		return summary, err
	}
	if err := r.cleanup(ctx); err != nil {
		return summary, err
	}
	if err := j.Finish("passed"); err != nil {
		return summary, err
	}
	summary.Status, summary.CleanupSucceeded = "passed", true
	return summary, nil
}

func atomicResult(path string, bz []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".result-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(bz); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (r *Runner) step(ctx context.Context, s *Summary, name string, output any, run func(context.Context, string) (any, error)) error {
	r.phase(s, name)
	return r.journal.Step(ctx, name, output, run)
}

func (r *Runner) workflow(ctx context.Context, s *Summary) error {
	var initial bootstrap
	if err := r.step(ctx, s, "preflight-five-distinct-machines", nil, func(ctx context.Context, _ string) (any, error) {
		machines := map[string]bool{}
		for _, h := range r.Inventory.Hosts {
			bz, err := r.Transport.Run(ctx, h, r.prefix()+"test ! -e \"$root\"\ntest \"$(uname -s)\" = Linux\nfor c in go git curl jq perl sha256sum ss flock; do command -v \"$c\" >/dev/null; done\nfor port in 26656 26657 1317 8081 8082 8083 9090; do test -z \"$(ss -H -ltn \"sport = :$port\")\"; done\nsudo -n iptables --version >/dev/null\ncat /etc/machine-id\n")
			if err != nil {
				return nil, err
			}
			id := strings.TrimSpace(string(bz))
			if id == "" || machines[id] {
				return nil, fmt.Errorf("hosts must report five distinct machine IDs")
			}
			machines[id] = true
		}
		return nil, nil
	}); err != nil {
		return err
	}
	if err := r.step(ctx, s, "provision", &initial, func(ctx context.Context, _ string) (any, error) {
		b := bootstrap{}
		seen := map[string]bool{}
		for _, h := range r.Inventory.Hosts {
			script := r.prefix() + "mkdir -p " + Quote(r.Inventory.RemoteBase) + "\nif test ! -d \"$root\"; then mkdir \"$root\"; printf '%s' \"$owner\" >\"$root/.g503-lab-owned\"; fi\ntest \"$(cat \"$root/.g503-lab-owned\")\" = \"$owner\"\nif test ! -d \"$root/source\"; then git clone --quiet --no-checkout -- " + Quote(r.Inventory.Repository) + " \"$root/source\"; fi\ntest \"$(git -C \"$root/source\" remote get-url origin)\" = " + Quote(r.Inventory.Repository) + "\ngit -C \"$root/source\" checkout --quiet --detach " + Quote(r.Inventory.Revision) + "\ntest \"$(git -C \"$root/source\" rev-parse HEAD)\" = " + Quote(r.Inventory.Revision) + "\nbash \"$root/source/dev/scripts/multihost-worker.sh\" \"$root\" \"$run\" \"$owner\" init " + Quote(r.Inventory.ChainID) + " " + Quote(h.ID) + "\n"
			if _, err := r.Transport.Run(ctx, h, script); err != nil {
				return nil, err
			}
			token := sha256.Sum256([]byte(r.owner + "/agent-api"))
			if err := r.put(ctx, h, "secret.json", map[string]string{"token": hex.EncodeToString(token[:])}); err != nil {
				return nil, err
			}
			bz, err := r.worker(ctx, h, "genesis")
			if err != nil {
				return nil, err
			}
			b.Genesis = append(b.Genesis, bz)
			bz, err = r.worker(ctx, h, "node-id")
			if err != nil {
				return nil, err
			}
			id := strings.TrimSpace(string(bz))
			decoded, e := hex.DecodeString(id)
			if e != nil || len(decoded) != 20 || seen[id] {
				return nil, fmt.Errorf("invalid or duplicate Comet node ID")
			}
			seen[id] = true
			b.NodeIDs = append(b.NodeIDs, id)
		}
		return b, nil
	}); err != nil {
		return err
	}
	if err := r.step(ctx, s, "start-five-validators", nil, func(ctx context.Context, _ string) (any, error) {
		combined, err := mergeGenesis(initial.Genesis)
		if err != nil {
			return nil, err
		}
		for n, h := range r.Inventory.Hosts {
			if err := r.put(ctx, h, "genesis.json", combined); err != nil {
				return nil, err
			}
			var peers []string
			for p, peer := range r.Inventory.Hosts {
				if p != n {
					peers = append(peers, initial.NodeIDs[p]+"@"+peer.PeerIP+":26656")
				}
			}
			if _, err := r.worker(ctx, h, "start-node", r.Inventory.ChainID, strings.Join(peers, ",")); err != nil {
				return nil, err
			}
		}
		return nil, r.wait(ctx, func() error { _, err := r.states(ctx); return err })
	}); err != nil {
		return err
	}
	first := r.Inventory.Hosts[0]
	if err := r.step(ctx, s, "start-five-agents", nil, func(ctx context.Context, _ string) (any, error) {
		if _, err := r.worker(ctx, first, "start-services"); err != nil {
			return nil, err
		}
		var endpoints []string
		for _, h := range r.Inventory.Hosts {
			endpoints = append(endpoints, "http://"+h.PeerIP+":26657")
		}
		for n, h := range r.Inventory.Hosts {
			config := map[string]any{"chain_id": r.Inventory.ChainID, "rpc_endpoints": endpoints, "rpc_url": endpoints[n], "rpc_safety": map[string]any{"max_block_age_seconds": 15, "max_height_lag": 2, "min_agreement": 3}, "agent_id": fmt.Sprintf("validator-%d", n+1), "key_file": r.root() + "/agent.key", "risk_provider": "mock", "risk_url": "http://" + first.PeerIP + ":8081", "llm_provider": "mock", "llm_url": "http://" + first.PeerIP + ":8082", "agent_api": map[string]any{"url": "http://" + first.PeerIP + ":8083", "role": "validator-agent", "token_env": "G503_AGENT_API_TOKEN"}, "broadcast": map[string]any{"gas": 700000, "fee": "0stake"}, "daemon": map[string]any{"enabled": true, "state_dir": r.root() + "/agent-state"}}
			if err := r.put(ctx, h, "agent.json", config); err != nil {
				return nil, err
			}
			if _, err := r.worker(ctx, h, "start-agent"); err != nil {
				return nil, err
			}
		}
		return nil, r.wait(ctx, func() error {
			for _, h := range r.Inventory.Hosts {
				if _, err := r.worker(ctx, h, "agent-ready"); err != nil {
					return err
				}
			}
			return nil
		})
	}); err != nil {
		return err
	}
	if err := r.step(ctx, s, "baseline-compensation", nil, func(ctx context.Context, id string) (any, error) { return nil, r.transferOperation(ctx, id) }); err != nil {
		return err
	}
	if err := r.step(ctx, s, "4-1-apply", nil, func(ctx context.Context, _ string) (any, error) { return nil, r.partition(ctx, 4) }); err != nil {
		return err
	}
	var hash string
	if err := r.step(ctx, s, "4-1-partition", &hash, func(ctx context.Context, id string) (any, error) {
		if err := r.pause(ctx, 20*time.Second); err != nil {
			return nil, err
		}
		before, err := r.states(ctx)
		if err != nil {
			return nil, err
		}
		hash, err := r.transfer(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := r.pause(ctx, 5*time.Second); err != nil {
			return nil, err
		}
		after, err := r.states(ctx)
		if err != nil {
			return nil, err
		}
		if after[0].height() <= before[0].height() || after[4].Sends != before[4].Sends {
			return nil, fmt.Errorf("4:1 majority must progress while isolated agent stops sends")
		}
		_, err = r.worker(ctx, first, "unsettled", hash)
		return hash, err
	}); err != nil {
		return err
	}
	if err := r.step(ctx, s, "4-1-heal", nil, func(ctx context.Context, _ string) (any, error) {
		if err := r.heal(ctx); err != nil {
			return nil, err
		}
		_, err := r.worker(ctx, r.Inventory.Hosts[4], "restart-agent")
		return nil, err
	}); err != nil {
		return err
	}
	if err := r.step(ctx, s, "4-1-settlement", nil, func(ctx context.Context, _ string) (any, error) {
		return nil, r.wait(ctx, func() error { _, err := r.worker(ctx, first, "settlement", hash); return err })
	}); err != nil {
		return err
	}
	if err := r.step(ctx, s, "3-2-apply", nil, func(ctx context.Context, _ string) (any, error) { return nil, r.partition(ctx, 3) }); err != nil {
		return err
	}
	var halted []nodeState
	if err := r.step(ctx, s, "3-2-halt-and-stale-send-suppression", &halted, func(ctx context.Context, _ string) (any, error) {
		if err := r.pause(ctx, 20*time.Second); err != nil {
			return nil, err
		}
		before, err := r.states(ctx)
		if err != nil {
			return nil, err
		}
		if err := r.pause(ctx, 5*time.Second); err != nil {
			return nil, err
		}
		after, err := r.states(ctx)
		if err != nil {
			return nil, err
		}
		for n := range after {
			if after[n].height() != before[n].height() || after[n].Sends != before[n].Sends {
				return nil, fmt.Errorf("3:2 split must halt blocks and stale agent sends")
			}
		}
		return after, nil
	}); err != nil {
		return err
	}
	if err := r.step(ctx, s, "3-2-heal", nil, func(ctx context.Context, _ string) (any, error) { return nil, r.heal(ctx) }); err != nil {
		return err
	}
	if err := r.step(ctx, s, "3-2-resumed", nil, func(ctx context.Context, _ string) (any, error) {
		return nil, r.wait(ctx, func() error {
			now, err := r.states(ctx)
			if err != nil {
				return err
			}
			if now[0].height() <= halted[0].height() {
				return fmt.Errorf("chain not resumed")
			}
			return nil
		})
	}); err != nil {
		return err
	}
	if err := r.step(ctx, s, "all-five-stop", nil, func(ctx context.Context, _ string) (any, error) {
		for _, h := range r.Inventory.Hosts {
			if _, err := r.worker(ctx, h, "stop-node"); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}); err != nil {
		return err
	}
	if err := r.step(ctx, s, "all-five-restart", nil, func(ctx context.Context, _ string) (any, error) {
		for _, h := range r.Inventory.Hosts {
			if _, err := r.worker(ctx, h, "restart-node"); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}); err != nil {
		return err
	}
	if err := r.step(ctx, s, "all-five-restart-and-persistence", nil, func(ctx context.Context, _ string) (any, error) {
		return nil, r.wait(ctx, func() error {
			for _, h := range r.Inventory.Hosts {
				if _, err := r.worker(ctx, h, "settlement", hash); err != nil {
					return err
				}
			}
			return nil
		})
	}); err != nil {
		return err
	}
	if err := r.step(ctx, s, "post-restart-compensation", nil, func(ctx context.Context, id string) (any, error) { return nil, r.transferOperation(ctx, id) }); err != nil {
		return err
	}
	return r.step(ctx, s, "common-block-convergence", nil, func(ctx context.Context, _ string) (any, error) {
		return nil, r.wait(ctx, func() error { return r.converged(ctx) })
	})
}

func (r *Runner) transfer(ctx context.Context, id string) (string, error) {
	bz, err := r.worker(ctx, r.Inventory.Hosts[0], "transfer", id)
	if err != nil {
		return "", err
	}
	var tx struct {
		Hash string `json:"tx_hash"`
	}
	if err := json.Unmarshal(bz, &tx); err != nil || len(tx.Hash) != 64 {
		return "", fmt.Errorf("invalid transfer response")
	}
	return tx.Hash, nil
}
func (r *Runner) transferOperation(ctx context.Context, id string) error {
	hash, err := r.transfer(ctx, id)
	if err != nil {
		return err
	}
	return r.wait(ctx, func() error { _, err := r.worker(ctx, r.Inventory.Hosts[0], "settlement", hash); return err })
}
func (r *Runner) cleanup(ctx context.Context) error {
	var errs []error
	for _, h := range r.Inventory.Hosts {
		script := r.prefix() + "if test -f \"$root/source/dev/scripts/multihost-worker.sh\" && test \"$(cat \"$root/.g503-lab-owned\" 2>/dev/null || true)\" = \"$owner\"; then bash \"$root/source/dev/scripts/multihost-worker.sh\" \"$root\" \"$run\" \"$owner\" cleanup; fi\n"
		if _, err := r.Transport.Run(ctx, h, script); err != nil {
			errs = append(errs, err)
			continue
		}
		archive, err := r.worker(ctx, h, "collect")
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := atomicResult(filepath.Join(r.ResultDir, h.ID+"-logs.tar.gz"), archive); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
