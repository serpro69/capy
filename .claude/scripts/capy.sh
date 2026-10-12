#!/usr/bin/env bash

# Go resolves knowledge credentials; vault credentials stay environment-only.
set -uo pipefail

codex_hook=false
codex_pretool=false
if [ "${1:-}" = "hook" ]; then
  previous=""
  for arg in "$@"; do
    if [ "$arg" = "--platform=codex" ] || { [ "$previous" = "--platform" ] && [ "$arg" = "codex" ]; }; then codex_hook=true; fi
    if [ "$arg" = "pretooluse" ]; then codex_pretool=true; fi
    previous="$arg"
  done
fi

p=""
for candidate in "$(command -v capy 2>/dev/null || true)" "$HOME/.local/bin/capy" "/opt/homebrew/bin/capy" "/usr/local/bin/capy" "$HOME/go/bin/capy" "capy"; do
  if [ -n "$candidate" ] && [ -f "$candidate" ] && [ -x "$candidate" ]; then
    p="$candidate"
    break
  fi
done
capy_run() {
  "$@" <&0 &
  capy_child=$!
  trap 'kill -TERM "$capy_child" 2>/dev/null; wait "$capy_child"; exit 143' TERM
  trap 'kill -INT "$capy_child" 2>/dev/null; wait "$capy_child"; exit 130' INT
  trap 'kill -HUP "$capy_child" 2>/dev/null; wait "$capy_child"; exit 129' HUP
  wait "$capy_child"
  capy_status=$?
  trap - TERM INT HUP
  return "$capy_status"
}
if [ -n "$p" ]; then
  if [ "$codex_hook" = true ]; then
    capy_run "$p" "$@"
    status=$?
    if [ "$status" -ne 0 ]; then
      echo "capy: Codex hook binary failed; install/upgrade capy or repair configuration from an external terminal and rerun capy setup --platform codex" >&2
      if [ "$codex_pretool" = true ]; then exit 2; fi
    fi
    exit "$status"
  fi
  if [ "${1:-}" = "hook" ]; then
    "$p" "$@" || true
    exit 0
  fi
  exec "$p" "$@"
fi

if [ "$codex_hook" = true ]; then
  echo "capy: Codex hook binary not found; install/upgrade capy from an external terminal and rerun capy setup --platform codex" >&2
  if [ "$codex_pretool" = true ]; then exit 2; fi
  exit 127
fi
if [ "${1:-}" = "hook" ]; then
  jq -n --arg reason "capy binary not found" \
    '{hookSpecificOutput: {hookEventName: "PreToolUse", permissionDecision: "deny", permissionDecisionReason: $reason}}'
  exit 0
fi

echo "capy: binary not found (searched PATH, \$HOME/.local/bin, /opt/homebrew/bin, /usr/local/bin, \$HOME/go/bin)" >&2
exit 127
