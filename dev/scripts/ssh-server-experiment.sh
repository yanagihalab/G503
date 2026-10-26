#!/usr/bin/env bash
set -euo pipefail

HOST=""
USER_NAME=""
PORT="22"
IDENTITY_FILE=""
REMOTE_DIR="g503-ssh-lab"
EXPERIMENT="agent-flow"
INSTALL_DEPS=0
SKIP_SYNC=0
LOCAL_RESULTS=".g503/ssh-results"

usage() {
  cat <<'USAGE'
Usage:
  bash dev/scripts/ssh-server-experiment.sh --host HOST [options]

Options:
  --host HOST              SSH host or IP address.
  --user USER              SSH user. Defaults to current local user.
  --port PORT              SSH port. Defaults to 22.
  --identity PATH          SSH private key path.
  --remote-dir PATH        Remote experiment directory. Defaults to g503-ssh-lab
                           under the SSH user's home directory.
  --experiment NAME        checks, agent-flow, local-e2e, five-validator, five-agent,
                           sanction-safety, agent-recovery, ownership-snapshot, validator-lifecycle, or all. Defaults to agent-flow.
  --install-deps           Attempt apt-get dependency install on the remote host.
  --skip-sync              Reuse existing remote-dir without rsync.
  --local-results PATH     Local directory for fetched results. Defaults to .g503/ssh-results.

Examples:
  bash dev/scripts/ssh-server-experiment.sh --host 203.0.113.10 --user ubuntu --experiment agent-flow
  bash dev/scripts/ssh-server-experiment.sh --host lab.example.jp --identity ~/.ssh/id_ed25519 --experiment all
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host)
      HOST="$2"
      shift 2
      ;;
    --user)
      USER_NAME="$2"
      shift 2
      ;;
    --port)
      PORT="$2"
      shift 2
      ;;
    --identity)
      IDENTITY_FILE="$2"
      shift 2
      ;;
    --remote-dir)
      REMOTE_DIR="$2"
      shift 2
      ;;
    --experiment)
      EXPERIMENT="$2"
      shift 2
      ;;
    --install-deps)
      INSTALL_DEPS=1
      shift
      ;;
    --skip-sync)
      SKIP_SYNC=1
      shift
      ;;
    --local-results)
      LOCAL_RESULTS="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "$HOST" ]]; then
  echo "--host is required" >&2
  usage >&2
  exit 2
fi
case "$EXPERIMENT" in
  checks|agent-flow|local-e2e|five-validator|five-agent|sanction-safety|agent-recovery|ownership-snapshot|validator-lifecycle|all) ;;
  *) echo "unknown experiment: $EXPERIMENT" >&2; exit 2 ;;
esac
if [[ ! "$HOST" =~ ^[A-Za-z0-9_.:-]+$ || "$HOST" == -* || ! "$PORT" =~ ^[0-9]+$ || "$PORT" -lt 1 || "$PORT" -gt 65535 ]]; then
  echo "invalid SSH host or port" >&2; exit 2
fi
if [[ ! "$REMOTE_DIR" =~ ^[A-Za-z0-9_./-]+$ || "/$REMOTE_DIR/" == *"/../"* || "$REMOTE_DIR" == "/" || "$REMOTE_DIR" == "." || "$REMOTE_DIR" == -* ]]; then
  echo "remote-dir must identify a dedicated experiment directory without spaces" >&2; exit 2
fi

if [[ -z "$USER_NAME" ]]; then
  USER_NAME="$(id -un)"
fi

if ! command -v ssh >/dev/null 2>&1; then
  echo "ssh is required locally" >&2
  exit 1
fi
if ! command -v rsync >/dev/null 2>&1; then
  echo "rsync is required locally for result collection" >&2
  exit 1
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
REMOTE="${USER_NAME}@${HOST}"
SSH_OPTS=(-p "$PORT" -o ServerAliveInterval=30 -o ServerAliveCountMax=6)
if [[ -n "$IDENTITY_FILE" ]]; then
  SSH_OPTS+=(-i "$IDENTITY_FILE")
fi
printf -v RSYNC_SSH '%q ' ssh "${SSH_OPTS[@]}"

remote_quote() {
  printf "%q" "$1"
}

REMOTE_DIR_QUOTED="$(remote_quote "$REMOTE_DIR")"

echo "[1/4] prepare remote directory: $REMOTE:$REMOTE_DIR"
ssh "${SSH_OPTS[@]}" "$REMOTE" "mkdir -p $REMOTE_DIR_QUOTED"

if [[ "$SKIP_SYNC" == "0" ]]; then
  echo "[2/4] sync repository"
  rsync -az \
    --exclude '.git/' \
    --exclude '.gocache/' \
    --exclude '.g503/' \
    --exclude '.env*' \
    --exclude '*.pem' \
    --exclude '*.key' \
    --exclude 'demo-keys.json' \
    --exclude '*key.hex' \
    --exclude 'ssh-results/' \
    -e "$RSYNC_SSH" \
    "$ROOT/" "$REMOTE:$REMOTE_DIR/"
else
  echo "[2/4] skip repository sync"
fi

setup_args=()
if [[ "$INSTALL_DEPS" == "1" ]]; then
  setup_args+=(--install-deps)
fi

echo "[3/4] run remote setup and experiment: $EXPERIMENT"
remote_status=0
ssh "${SSH_OPTS[@]}" "$REMOTE" "cd $REMOTE_DIR_QUOTED && bash dev/scripts/ssh-remote-setup.sh ${setup_args[*]} && G503_SSH_RUN_ID=$RUN_ID bash dev/scripts/ssh-remote-run.sh --experiment $(remote_quote "$EXPERIMENT")" || remote_status=$?

echo "[4/4] fetch results"
mkdir -p "$LOCAL_RESULTS/$HOST-$RUN_ID"
fetch_status=0
rsync -az \
  -e "$RSYNC_SSH" \
  "$REMOTE:$REMOTE_DIR/ssh-results/$RUN_ID/" "$LOCAL_RESULTS/$HOST-$RUN_ID/" || fetch_status=$?
if [[ "$remote_status" != "0" ]]; then
  echo "remote experiment failed (exit $remote_status); available logs: $LOCAL_RESULTS/$HOST-$RUN_ID" >&2
  exit "$remote_status"
fi
if [[ "$fetch_status" != "0" ]]; then exit "$fetch_status"; fi

cat <<EOF
ssh experiment completed
remote: $REMOTE:$REMOTE_DIR/ssh-results/$RUN_ID
local:  $LOCAL_RESULTS/$HOST-$RUN_ID
EOF
