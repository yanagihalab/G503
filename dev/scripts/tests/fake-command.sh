#!/usr/bin/env bash
set -eu
case "${0##*/}" in
  go)
    case "$1" in
      env) printf 'go%s\n' "${G503_TEST_GO_VERSION:-1.26.4}" ;;
      test) echo "simulated Go test failure"; exit "${G503_TEST_GO_EXIT:-0}" ;;
      *) exit 2 ;;
    esac
    ;;
  ssh)
    printf 'ssh %s\n' "$*" >>"$G503_TEST_CALL_LOG"
    case "$*" in *ssh-remote-setup.sh*) exit 7 ;; esac
    ;;
  rsync) printf 'rsync %s\n' "$*" >>"$G503_TEST_CALL_LOG" ;;
esac
