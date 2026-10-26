# Five-Host Fault Experiment

`g503-multihost` is a destructive research-lab runner, not a production deployer.
It uses five real Linux machines, one validator and one resident agent per host.
Central risk, LLM and Agent API processes are mocks on host1. No real Chainalysis
or model credentials are required. Existing single-server SSH helpers remain
available; five local processes are not equivalent to this experiment.

## Prerequisites

- Five dedicated Linux hosts with unique `/etc/machine-id`, distinct IPv4 peer
  addresses, Bash, Git, Go matching `go.mod`, curl, jq, Perl, tar, sha256sum and
  `ss` (iproute2). Preflight refuses occupied service ports, including local gRPC 9090.
- Noninteractive SSH access with host keys already verified in `known_hosts`.
  Agent forwarding and host-key verification bypasses are not used.
- The SSH user must have `sudo -n iptables` permission for fault injection.
  Restrict this to disposable isolated machines, not shared production hosts.
- Hosts must fetch the public HTTPS repository and the full pushed commit SHA.
  Private repository token distribution is intentionally not implemented.
- TCP 26656, 26657, 1317, 8081, 8082 and 8083 must be unused and reachable
  between experiment peers. SSH must use a different port. RPC/API/mock services
  bind to all IPv4 interfaces: firewall the lab from the public internet.
- Synchronized host clocks; retained blocks/results; sufficient CPU, RAM and
  disk to build Cosmos SDK on each host. The controller defaults to 90 minutes
  overall (`-timeout`), with two-minute readiness/recovery phase deadlines.

## Plan and Execute

Create an inventory matching `dev/multihost.example.json`. Use real SSH targets,
private peer IPv4 addresses, SSH ports and optional local `identity_file` paths.
Do not put passwords, private keys or tokens into inventory. Set a unique
`run_id`, a new chain ID and a dedicated `remote_base`. The example SHA of all
zeros is invalid; use a full commit already pushed to the configured repository.

```bash
# No SSH or mutation; validates inventory and prints the proposed phases.
go run ./dev/agent/cmd/g503-multihost \
  -inventory /path/to/five-hosts.json -revision "$(git rev-parse HEAD)"

# Explicit opt-in: provisions machines and injects scoped firewall rules.
go run ./dev/agent/cmd/g503-multihost \
  -inventory /path/to/five-hosts.json -revision "$(git rev-parse HEAD)" \
  -execute -results /path/to/new-results -timeout 90m
```

The local results directory and `<remote_base>/<run_id>` must not already exist.
No existing checkout or journal is overwritten. Each invocation uses a random
ownership marker, so an old controller cannot clean up a newer run with the
same path. Generated keys and API token remain mode `0600` on remote hosts.
Only generated demonstration accounts are used; the send helper exposes a demo
key in its remote process arguments. Never substitute funded production keys.
HTTP mock API traffic is private-lab-only, not an mTLS deployment.

## Checks

1. Validate five distinct machines and initialize a shared genesis with five
   validators/agents. Start five guarded RPC clients requiring three matching
   endpoints, then confirm a baseline automatic compensation.
2. Split 4:1 using a dedicated iptables chain. Majority blocks must advance,
   isolated-agent broadcasts must stop and the missing fifth vote must prevent
   settlement. Heal, restart the isolated agent with its journal and require
   exactly one settlement, five votes and exact sender/recipient balances.
3. Split 3:2. With less than two-thirds voting power, blocks must stop. After
   the 15-second freshness bound, broadcast counters must remain unchanged.
   Heal and require block production to resume.
4. Stop/restart all five nodes without deleting data. Check the prior settlement
   on every node, process another transfer and compare all hosts' block hashes
   at one common height.
5. Stop owned processes, remove only owned firewall rules and collect logs.

Partition rules match both source and destination TCP service ports, including
established connections and replies. They do not block SSH, change global
policies, flush unrelated chains or alter UDP. Custom latency/loss/netem,
IPv6-only hosts, Byzantine RPC behavior and remote signing-key failover are not
covered by this initial scenario.

## Evidence and Recovery

`summary.json` records the revision, attempted phases, final status, cleanup
result and whether actual SSH transport ran. `*-logs.tar.gz` contains remote
`logs/` only, not keys, tokens, config or journals. A simulated controller run
never sets `real_hosts_executed` to true. `planned` is not a successful E2E.

Cleanup runs on errors and catchable interrupt/termination, with a separate
bounded context. Controller SIGKILL, machine loss or an unreachable SSH service
cannot guarantee automatic cleanup. Once reachable, use the original remote
ownership marker for manual recovery on each host:

```bash
root=/var/tmp/g503-lab/YOUR_RUN_ID
owner="$(cat "$root/.g503-lab-owned")"
bash "$root/source/dev/scripts/multihost-worker.sh" \
  "$root" YOUR_RUN_ID "$owner" cleanup
```

Inspect results before manually removing experiment directories. Cleanup retains
data; it never deletes the remote root. Use a new run ID for the next run.
If provisioning failed before the worker was downloaded, inspect the owned root
manually; no global cleanup command is provided. Do not run `iptables -F` or
`killall` as a workaround. SSH failures and incomplete cleanup fail the run.

## Verification Boundary

Automated tests cover inventory validation, plan-only behavior, simulated
4:1/3:2/restart orchestration, error cleanup, ownership refusal, firewall command
scope and key-free log archives. Local five-validator/five-agent tests exercise
actual chain processes and preferred-RPC failover. Five real SSH hosts have not
been exercised until valid host inventory is supplied and an executed summary
and logs are inspected. CI intentionally performs no credentialed SSH mutation.
