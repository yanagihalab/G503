package multihost

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Host struct {
	ID       string `json:"id"`
	SSH      string `json:"ssh"`
	Port     int    `json:"port"`
	Identity string `json:"identity_file,omitempty"`
	PeerIP   string `json:"peer_ip"`
}

type Inventory struct {
	RunID      string `json:"run_id"`
	ChainID    string `json:"chain_id"`
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	RemoteBase string `json:"remote_base"`
	Hosts      []Host `json:"hosts"`
}

var word = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,47}$`)
var sshTarget = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.@-]*$`)
var revision = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
var remotePath = regexp.MustCompile(`^/[a-zA-Z0-9_/-]+$`)

func Load(path string) (Inventory, error) {
	var inventory Inventory
	file, err := os.Open(path)
	if err != nil {
		return inventory, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return inventory, err
	}
	return inventory, nil
}

func (i Inventory) Validate() error {
	u, err := url.Parse(i.Repository)
	if !word.MatchString(i.RunID) || !word.MatchString(i.ChainID) || !revision.MatchString(i.Revision) || strings.Trim(i.Revision, "0") == "" || err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("valid run/chain IDs, HTTPS repository and full nonzero commit SHA are required")
	}
	if !remotePath.MatchString(i.RemoteBase) || filepath.Clean(i.RemoteBase) != i.RemoteBase || i.RemoteBase == "/" || len(i.Hosts) != 5 {
		return fmt.Errorf("a dedicated absolute remote_base and exactly five hosts are required")
	}
	seenID, seenSSH, seenIP := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, h := range i.Hosts {
		ip := net.ParseIP(h.PeerIP)
		if !word.MatchString(h.ID) || !sshTarget.MatchString(h.SSH) || strings.Count(h.SSH, "@") > 1 || ip == nil || ip.To4() == nil || h.Port < 1 || h.Port > 65535 || seenID[h.ID] || seenSSH[h.SSH] || seenIP[h.PeerIP] {
			return fmt.Errorf("five distinct IDs, SSH targets and IPv4 peer addresses are required")
		}
		for _, port := range []int{26656, 26657, 1317, 8081, 8082, 8083} {
			if h.Port == port {
				return fmt.Errorf("SSH port must not overlap lab ports")
			}
		}
		seenID[h.ID], seenSSH[h.SSH], seenIP[h.PeerIP] = true, true, true
	}
	return nil
}

func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

type Transport interface {
	Run(context.Context, Host, string) ([]byte, error)
}

type SSHTransport struct{}

func (SSHTransport) Run(ctx context.Context, h Host, script string) ([]byte, error) {
	args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ConnectTimeout=10", "-p", strconv.Itoa(h.Port)}
	if h.Identity != "" {
		args = append(args, "-i", h.Identity)
	}
	args = append(args, "--", h.SSH, "bash", "-se")
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = strings.NewReader(script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("host %s SSH failed: %w: %s", h.ID, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

type Summary struct {
	Status            string   `json:"status"`
	Revision          string   `json:"revision"`
	RealHostsExecuted bool     `json:"real_hosts_executed"`
	Phases            []string `json:"phases"`
	Error             string   `json:"error,omitempty"`
	CleanupSucceeded  bool     `json:"cleanup_succeeded"`
}

type Runner struct {
	Inventory Inventory
	Transport Transport
	ResultDir string
	Progress  func(string)
	owner     string
	sleep     func(context.Context, time.Duration) error
}

func (r *Runner) root() string { return r.Inventory.RemoteBase + "/" + r.Inventory.RunID }
func (r *Runner) prefix() string {
	return "set -euo pipefail\numask 077\nroot=" + Quote(r.root()) + "\nrun=" + Quote(r.Inventory.RunID) + "\nowner=" + Quote(r.owner) + "\n"
}
func (r *Runner) worker(ctx context.Context, h Host, action string, args ...string) ([]byte, error) {
	script := r.prefix() + "bash \"$root/source/dev/scripts/multihost-worker.sh\" \"$root\" \"$run\" \"$owner\" " + Quote(action)
	for _, arg := range args {
		script += " " + Quote(arg)
	}
	return r.Transport.Run(ctx, h, script+"\n")
}
func (r *Runner) put(ctx context.Context, h Host, name string, value any) error {
	bz, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = r.Transport.Run(ctx, h, r.prefix()+"test \"$(cat \"$root/.g503-lab-owned\")\" = \"$owner\"\ncat > \"$root/"+name+"\" <<'G503_JSON_EOF'\n"+string(bz)+"\nG503_JSON_EOF\n")
	return err
}
func (r *Runner) phase(s *Summary, name string) {
	s.Phases = append(s.Phases, name)
	if r.Progress != nil {
		r.Progress(name)
	}
}

func (r *Runner) Plan() (Summary, error) {
	if err := r.Inventory.Validate(); err != nil {
		return Summary{}, err
	}
	return Summary{Status: "planned", Revision: r.Inventory.Revision, Phases: []string{"preflight-five-distinct-machines", "fresh-isolated-provision", "shared-genesis-five-validators", "five-resident-agents", "baseline-compensation", "4-1-partition-and-heal", "3-2-halt-stale-send-suppression-and-heal", "all-five-restart-and-persistence", "scoped-cleanup-and-log-collection"}}, nil
}

func (r *Runner) Execute(ctx context.Context) (summary Summary, retErr error) {
	if _, err := r.Plan(); err != nil {
		return summary, err
	}
	if r.Transport == nil {
		return summary, fmt.Errorf("SSH transport is required")
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return summary, err
	}
	r.owner = r.Inventory.RunID + "-" + hex.EncodeToString(nonce[:])
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return summary, err
	}
	if err := os.Mkdir(r.ResultDir, 0700); err != nil {
		return summary, err
	}
	_, realSSH := r.Transport.(SSHTransport)
	summary = Summary{Status: "failed", Revision: r.Inventory.Revision, RealHostsExecuted: realSSH}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var cleanupErrors []error
		for _, h := range r.Inventory.Hosts {
			// No global firewall flush, process-name kill or remote directory deletion.
			script := r.prefix() + "if test -f \"$root/source/dev/scripts/multihost-worker.sh\" && test \"$(cat \"$root/.g503-lab-owned\" 2>/dev/null || true)\" = \"$owner\"; then bash \"$root/source/dev/scripts/multihost-worker.sh\" \"$root\" \"$run\" \"$owner\" cleanup; fi\n"
			if _, err := r.Transport.Run(cleanupCtx, h, script); err != nil {
				cleanupErrors = append(cleanupErrors, err)
				continue
			}
			archive, err := r.worker(cleanupCtx, h, "collect")
			if err != nil {
				cleanupErrors = append(cleanupErrors, err)
			} else if err = os.WriteFile(filepath.Join(r.ResultDir, h.ID+"-logs.tar.gz"), archive, 0600); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
		summary.CleanupSucceeded = len(cleanupErrors) == 0
		retErr = errors.Join(retErr, errors.Join(cleanupErrors...))
		if retErr == nil {
			summary.Status = "passed"
		} else {
			summary.Error = retErr.Error()
		}
		bz, err := json.MarshalIndent(summary, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(r.ResultDir, "summary.json"), bz, 0600)
		}
		retErr = errors.Join(retErr, err)
	}()
	r.phase(&summary, "preflight-five-distinct-machines")
	machines := map[string]bool{}
	for _, h := range r.Inventory.Hosts {
		bz, err := r.Transport.Run(ctx, h, r.prefix()+"test ! -e \"$root\"\ntest \"$(uname -s)\" = Linux\nfor c in go git curl jq perl sha256sum ss; do command -v \"$c\" >/dev/null; done\nfor port in 26656 26657 1317 8081 8082 8083 9090; do test -z \"$(ss -H -ltn \"sport = :$port\")\"; done\nsudo -n iptables --version >/dev/null\ncat /etc/machine-id\n")
		if err != nil {
			return summary, err
		}
		id := strings.TrimSpace(string(bz))
		if id == "" || machines[id] {
			return summary, fmt.Errorf("hosts must report five distinct machine IDs")
		}
		machines[id] = true
	}
	r.phase(&summary, "provision")
	genesis := make([][]byte, 5)
	nodeIDs := make([]string, 5)
	seenNodes := map[string]bool{}
	for n, h := range r.Inventory.Hosts {
		script := r.prefix() + "mkdir -p " + Quote(r.Inventory.RemoteBase) + "\nmkdir \"$root\"\nprintf '%s' \"$owner\" > \"$root/.g503-lab-owned\"\ngit clone --quiet --no-checkout -- " + Quote(r.Inventory.Repository) + " \"$root/source\"\ngit -C \"$root/source\" checkout --quiet --detach " + Quote(r.Inventory.Revision) + "\ntest \"$(git -C \"$root/source\" rev-parse HEAD)\" = " + Quote(r.Inventory.Revision) + "\nbash \"$root/source/dev/scripts/multihost-worker.sh\" \"$root\" \"$run\" \"$owner\" init " + Quote(r.Inventory.ChainID) + " " + Quote(h.ID) + "\n"
		if _, err := r.Transport.Run(ctx, h, script); err != nil {
			return summary, err
		}
		if err := r.put(ctx, h, "secret.json", map[string]string{"token": hex.EncodeToString(token[:])}); err != nil {
			return summary, err
		}
		bz, err := r.worker(ctx, h, "genesis")
		if err != nil {
			return summary, err
		}
		genesis[n] = bz
		bz, err = r.worker(ctx, h, "node-id")
		if err != nil {
			return summary, err
		}
		nodeIDs[n] = strings.TrimSpace(string(bz))
		decoded, decodeErr := hex.DecodeString(nodeIDs[n])
		if decodeErr != nil || len(decoded) != 20 || seenNodes[nodeIDs[n]] {
			return summary, fmt.Errorf("invalid or duplicate Comet node ID")
		}
		seenNodes[nodeIDs[n]] = true
	}
	combined, err := mergeGenesis(genesis)
	if err != nil {
		return summary, err
	}
	endpoints := make([]string, 5)
	for n, h := range r.Inventory.Hosts {
		endpoints[n] = "http://" + h.PeerIP + ":26657"
	}
	r.phase(&summary, "start-five-validators")
	for n, h := range r.Inventory.Hosts {
		if err := r.put(ctx, h, "genesis.json", combined); err != nil {
			return summary, err
		}
		peers := []string{}
		for p, peer := range r.Inventory.Hosts {
			if p != n {
				peers = append(peers, nodeIDs[p]+"@"+peer.PeerIP+":26656")
			}
		}
		if _, err := r.worker(ctx, h, "start-node", r.Inventory.ChainID, strings.Join(peers, ",")); err != nil {
			return summary, err
		}
	}
	if err := r.wait(ctx, func() error { _, err := r.states(ctx); return err }); err != nil {
		return summary, err
	}
	first := r.Inventory.Hosts[0]
	serviceIP := first.PeerIP
	if _, err := r.worker(ctx, first, "start-services"); err != nil {
		return summary, err
	}
	r.phase(&summary, "start-five-agents")
	for n, h := range r.Inventory.Hosts {
		config := map[string]any{"chain_id": r.Inventory.ChainID, "rpc_endpoints": endpoints, "rpc_url": endpoints[n], "rpc_safety": map[string]any{"max_block_age_seconds": 15, "max_height_lag": 2, "min_agreement": 3},
			"agent_id": fmt.Sprintf("validator-%d", n+1), "key_file": r.root() + "/agent.key", "risk_provider": "mock", "risk_url": "http://" + serviceIP + ":8081", "llm_provider": "mock", "llm_url": "http://" + serviceIP + ":8082",
			"agent_api": map[string]any{"url": "http://" + serviceIP + ":8083", "role": "validator-agent", "token_env": "G503_AGENT_API_TOKEN"}, "broadcast": map[string]any{"gas": 700000, "fee": "0stake"}, "daemon": map[string]any{"enabled": true, "state_dir": r.root() + "/agent-state"}}
		if err := r.put(ctx, h, "agent.json", config); err != nil {
			return summary, err
		}
		if _, err := r.worker(ctx, h, "start-agent"); err != nil {
			return summary, err
		}
	}
	if err := r.wait(ctx, func() error {
		for _, h := range r.Inventory.Hosts {
			if _, err := r.worker(ctx, h, "agent-ready"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return summary, err
	}
	r.phase(&summary, "baseline-compensation")
	if err := r.transferAndWait(ctx); err != nil {
		return summary, err
	}
	r.phase(&summary, "4-1-partition")
	if err := r.partition(ctx, 4); err != nil {
		return summary, err
	}
	if err := r.pause(ctx, 20*time.Second); err != nil {
		return summary, err
	}
	before, err := r.states(ctx)
	if err != nil {
		return summary, err
	}
	bz, err := r.worker(ctx, first, "transfer")
	if err != nil {
		return summary, err
	}
	var transfer struct {
		TxHash string `json:"tx_hash"`
	}
	if err = json.Unmarshal(bz, &transfer); err != nil {
		return summary, err
	}
	if err := r.pause(ctx, 5*time.Second); err != nil {
		return summary, err
	}
	after, err := r.states(ctx)
	if err != nil {
		return summary, err
	}
	if after[0].height() <= before[0].height() || after[4].Sends != before[4].Sends {
		return summary, fmt.Errorf("4:1 majority must progress while isolated agent stops sends")
	}
	if _, err := r.worker(ctx, first, "unsettled", transfer.TxHash); err != nil {
		return summary, err
	}
	if err := r.heal(ctx); err != nil {
		return summary, err
	}
	if _, err := r.worker(ctx, r.Inventory.Hosts[4], "restart-agent"); err != nil {
		return summary, err
	}
	if err := r.wait(ctx, func() error { _, err := r.worker(ctx, first, "settlement", transfer.TxHash); return err }); err != nil {
		return summary, err
	}
	r.phase(&summary, "3-2-halt-and-stale-send-suppression")
	if err := r.partition(ctx, 3); err != nil {
		return summary, err
	}
	if err := r.pause(ctx, 20*time.Second); err != nil {
		return summary, err
	}
	before, err = r.states(ctx)
	if err != nil {
		return summary, err
	}
	if err := r.pause(ctx, 5*time.Second); err != nil {
		return summary, err
	}
	after, err = r.states(ctx)
	if err != nil {
		return summary, err
	}
	for n := range after {
		if after[n].height() != before[n].height() || after[n].Sends != before[n].Sends {
			return summary, fmt.Errorf("3:2 split must halt blocks and stale agent sends")
		}
	}
	if err := r.heal(ctx); err != nil {
		return summary, err
	}
	if err := r.wait(ctx, func() error {
		now, err := r.states(ctx)
		if err != nil {
			return err
		}
		if now[0].height() <= after[0].height() {
			return fmt.Errorf("chain not resumed")
		}
		return nil
	}); err != nil {
		return summary, err
	}
	r.phase(&summary, "all-five-restart-and-persistence")
	for _, h := range r.Inventory.Hosts {
		if _, err := r.worker(ctx, h, "stop-node"); err != nil {
			return summary, err
		}
	}
	for _, h := range r.Inventory.Hosts {
		if _, err := r.worker(ctx, h, "restart-node"); err != nil {
			return summary, err
		}
	}
	if err := r.wait(ctx, func() error {
		for _, h := range r.Inventory.Hosts {
			if _, err := r.worker(ctx, h, "settlement", transfer.TxHash); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return summary, err
	}
	if err := r.transferAndWait(ctx); err != nil {
		return summary, err
	}
	if err := r.wait(ctx, func() error { return r.converged(ctx) }); err != nil {
		return summary, err
	}
	return summary, nil
}

func (r *Runner) converged(ctx context.Context) error {
	states, err := r.states(ctx)
	if err != nil {
		return err
	}
	height := states[0].height()
	for _, s := range states {
		if s.height() < height {
			height = s.height()
		}
	}
	var hash string
	for _, host := range r.Inventory.Hosts {
		bz, err := r.worker(ctx, host, "block-hash", strconv.FormatUint(height, 10))
		if err != nil {
			return err
		}
		next := strings.TrimSpace(string(bz))
		decoded, err := hex.DecodeString(next)
		if err != nil || len(decoded) != 32 || (hash != "" && !strings.EqualFold(hash, next)) {
			return fmt.Errorf("five hosts disagree on common block %d", height)
		}
		hash = next
	}
	return nil
}

type nodeState struct {
	Status struct {
		Result struct {
			Sync struct {
				Height string `json:"latest_block_height"`
			} `json:"sync_info"`
		} `json:"result"`
	} `json:"status"`
	Sends int `json:"sends"`
}

func (s nodeState) height() uint64 {
	h, _ := strconv.ParseUint(s.Status.Result.Sync.Height, 10, 64)
	return h
}
func (r *Runner) states(ctx context.Context) ([]nodeState, error) {
	states := make([]nodeState, 5)
	for n, h := range r.Inventory.Hosts {
		bz, err := r.worker(ctx, h, "state")
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(bz, &states[n]); err != nil || states[n].height() == 0 {
			return nil, fmt.Errorf("invalid node state on %s", h.ID)
		}
	}
	return states, nil
}
func (r *Runner) partition(ctx context.Context, split int) error {
	for n, h := range r.Inventory.Hosts {
		blocked := []string{}
		for p, peer := range r.Inventory.Hosts {
			if (n < split) != (p < split) {
				blocked = append(blocked, peer.PeerIP)
			}
		}
		if _, err := r.worker(ctx, h, "partition", blocked...); err != nil {
			return err
		}
	}
	return nil
}
func (r *Runner) heal(ctx context.Context) error {
	for _, h := range r.Inventory.Hosts {
		if _, err := r.worker(ctx, h, "heal"); err != nil {
			return err
		}
	}
	return nil
}
func (r *Runner) transferAndWait(ctx context.Context) error {
	bz, err := r.worker(ctx, r.Inventory.Hosts[0], "transfer")
	if err != nil {
		return err
	}
	var tx struct {
		Hash string `json:"tx_hash"`
	}
	if err = json.Unmarshal(bz, &tx); err != nil || len(tx.Hash) != 64 {
		return fmt.Errorf("invalid transfer response")
	}
	return r.wait(ctx, func() error { _, err := r.worker(ctx, r.Inventory.Hosts[0], "settlement", tx.Hash); return err })
}
func (r *Runner) wait(ctx context.Context, check func() error) error {
	deadline := time.Now().Add(2 * time.Minute)
	var last error
	for time.Now().Before(deadline) {
		if last = check(); last == nil {
			return nil
		}
		if err := r.pause(ctx, time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("phase timed out: %w", last)
}
func (r *Runner) pause(ctx context.Context, d time.Duration) error {
	if r.sleep != nil {
		return r.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func mergeGenesis(documents [][]byte) (result map[string]any, err error) {
	defer func() {
		if p := recover(); p != nil {
			result = nil
			err = fmt.Errorf("invalid fresh genesis structure: %v", p)
		}
	}()
	var base map[string]any
	accounts, balances := map[string]any{}, map[string]any{}
	var validators, agents []any
	for n, bz := range documents {
		var doc map[string]any
		d := json.NewDecoder(bytes.NewReader(bz))
		d.UseNumber()
		if err := d.Decode(&doc); err != nil {
			return nil, err
		}
		if n == 0 {
			base = doc
		}
		vs, ok := doc["validators"].([]any)
		if !ok || len(vs) != 1 {
			return nil, fmt.Errorf("each fresh genesis needs one validator")
		}
		validators = append(validators, vs[0])
		app, ok := doc["app_state"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("missing app state")
		}
		for _, a := range app["auth"].(map[string]any)["accounts"].([]any) {
			m := a.(map[string]any)
			address, _ := m["address"].(string)
			if address == "" {
				address = m["base_account"].(map[string]any)["address"].(string)
			}
			accounts[address] = m
		}
		for _, b := range app["bank"].(map[string]any)["balances"].([]any) {
			m := b.(map[string]any)
			balances[m["address"].(string)] = m
		}
		a := app["sanction"].(map[string]any)["agents"].([]any)[0].(map[string]any)
		a["agent_id"] = fmt.Sprintf("validator-%d", n+1)
		agents = append(agents, a)
	}
	keys := func(m map[string]any) []string {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}
	var accountRows, balanceRows []any
	for n, k := range keys(accounts) {
		m := accounts[k].(map[string]any)
		if m["address"] != nil {
			m["account_number"] = strconv.Itoa(n + 10)
			m["sequence"] = "0"
		}
		accountRows = append(accountRows, m)
	}
	supply := new(big.Int)
	for _, k := range keys(balances) {
		m := balances[k].(map[string]any)
		for _, coin := range m["coins"].([]any) {
			c := coin.(map[string]any)
			v, ok := new(big.Int).SetString(c["amount"].(string), 10)
			if !ok || c["denom"] != "stake" {
				return nil, fmt.Errorf("invalid demo balance")
			}
			supply.Add(supply, v)
		}
		balanceRows = append(balanceRows, m)
	}
	base["validators"] = validators
	app := base["app_state"].(map[string]any)
	app["auth"].(map[string]any)["accounts"] = accountRows
	app["bank"].(map[string]any)["balances"] = balanceRows
	app["bank"].(map[string]any)["supply"] = []any{map[string]any{"denom": "stake", "amount": supply.String()}}
	sanction := app["sanction"].(map[string]any)
	sanction["agents"] = agents
	p := sanction["params"].(map[string]any)
	p["evidence_ttl_blocks"] = "300"
	p["voting_period_blocks"] = "100"
	return base, nil
}
