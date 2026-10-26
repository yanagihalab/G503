#!/usr/bin/env bash
set -euo pipefail

INSTALL_DEPS=0
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
REQUIRED_GO="$(awk '$1 == "go" { print $2; exit }' "$ROOT_DIR/go.mod")"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --install-deps)
      INSTALL_DEPS=1
      shift
      ;;
    -h|--help)
      cat <<'USAGE'
Usage: bash dev/scripts/ssh-remote-setup.sh [--install-deps]

Checks an SSH server for the dependencies needed to run G503 experiments.
With --install-deps, attempts to install common packages through apt-get.
Go itself is checked against go.mod and is not downloaded automatically.
USAGE
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      exit 2
      ;;
  esac
done

if [[ "$INSTALL_DEPS" == "1" ]]; then
  if command -v apt-get >/dev/null 2>&1; then
    if command -v sudo >/dev/null 2>&1; then
      sudo apt-get update
      sudo apt-get install -y build-essential ca-certificates curl git jq perl xxd rsync
    else
      apt-get update
      apt-get install -y build-essential ca-certificates curl git jq perl xxd rsync
    fi
  else
    echo "--install-deps currently supports apt-get based Linux servers only" >&2
    exit 1
  fi
fi

missing=()
for bin in bash curl git go jq perl xxd rsync; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    missing+=("$bin")
  fi
done

if [[ "${#missing[@]}" -gt 0 ]]; then
  echo "missing required commands: ${missing[*]}" >&2
  echo "Install them on the SSH server, then rerun this setup check." >&2
  exit 1
fi

go_version="$(GOTOOLCHAIN=local go env GOVERSION)"
go_version="${go_version#go}"
if ! awk -v actual="$go_version" -v required="$REQUIRED_GO" 'BEGIN {
  split(actual, a, "."); split(required, r, ".");
  for (i = 1; i <= 3; i++) {
    if (a[i]+0 > r[i]+0) exit 0;
    if (a[i]+0 < r[i]+0) exit 1;
  }
}' ; then
  echo "Go >= $REQUIRED_GO is required by go.mod; found $go_version" >&2
  exit 1
fi

cat <<EOF
ssh remote setup ok
host: $(hostname)
go: $(go env GOVERSION)
goos: $(go env GOOS)
goarch: $(go env GOARCH)
EOF
