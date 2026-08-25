#!/usr/bin/env bash
# migrate-v2.sh — the deterministic half of the v2 cutover (#269):
#   stop your server first, run this, restart, then verify by hand
#   (browse around; optionally run the self-parity health crawl printed
#   at the end).
#
# What it does, in order — each step gates the next:
#   1. refuses a running server
#   2. gridwell backup            → ~/gridwell-backups/pre-v2-<name>-<stamp>
#   3. gridwell convert-v2        → <home>-v2   (offline, never in place)
#   4. THE PARITY GATE: serves the old and converted homes side by side
#      on loopback ports and crawls both to zero differences (scoped by
#      convert-scope.txt; only live dial status and cache stamping are
#      ignored). Any difference aborts before anything is swapped.
#   5. swaps: <home> → ~/gridwell-backups/v1-home-<name>-<stamp> (the
#      rollback), <home>-v2 → <home>
#
# Usage: scripts/migrate-v2.sh [GRIDWELL_HOME]   (default ~/.gridwell)

set -euo pipefail

HOME_DIR="${1:-$HOME/.gridwell}"
HOME_DIR="${HOME_DIR%/}"
NAME="$(basename "$HOME_DIR" | sed 's/^\.//')"
STAMP="$(date +%Y%m%d-%H%M%S)"
BACKUPS="$HOME/gridwell-backups"
REPO="$(cd "$(dirname "$0")/.." && pwd)"
GW="$REPO/gridwell"
PORT_A=39001
PORT_B=39002

[ -d "$HOME_DIR" ] || { echo "migrate-v2: no home at $HOME_DIR"; exit 1; }
[ -x "$GW" ] || { echo "migrate-v2: no gridwell binary at $GW — run 'make build' in $REPO first"; exit 1; }

# 1. The source must be quiescent.
if GRIDWELL_HOME="$HOME_DIR" "$GW" status >/dev/null 2>&1; then
  echo "migrate-v2: a server is RUNNING on $HOME_DIR — stop it first (ctrl-c in its tmux window)"
  exit 1
fi

# 2. Backup (VACUUM INTO — a clean snapshot even with WAL siblings).
mkdir -p "$BACKUPS"
echo "== backup → $BACKUPS/pre-v2-$NAME-$STAMP"
GRIDWELL_HOME="$HOME_DIR" "$GW" backup "$BACKUPS/pre-v2-$NAME-$STAMP"

# 3. Convert.
V2="$HOME_DIR-v2"
[ -e "$V2" ] && { echo "migrate-v2: $V2 already exists — remove it or finish a previous run"; exit 1; }
echo "== convert-v2 → $V2"
"$GW" convert-v2 --from "$HOME_DIR" --to "$V2"

# 4. The parity gate: old vs converted, real serve processes.
PASSWORD="$(grep -E '^password:' "$HOME_DIR/server.yaml" | awk '{print $2}' || true)"
PW_ARGS=()
[ -n "$PASSWORD" ] && PW_ARGS=(--password "$PASSWORD")
# Expansions of a possibly-EMPTY array must use the ${arr[@]+"${arr[@]}"}
# idiom: macOS ships bash 3.2, where a plain "${arr[@]}" on an empty
# array is an "unbound variable" error under set -u (fixed in bash 4.4).

OLD_PID=""
NEW_PID=""
cleanup() {
  [ -n "$OLD_PID" ] && kill "$OLD_PID" 2>/dev/null || true
  [ -n "$NEW_PID" ] && kill "$NEW_PID" 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT

# The --ignore-fields list is parity.ConvertGateIgnoreFields — each entry a
# deliberate, named blind spot — and a test pins this script to it.
echo "== parity gate (old :$PORT_A vs converted :$PORT_B)"
GRIDWELL_HOME="$HOME_DIR" "$GW" serve --bind "127.0.0.1:$PORT_A" >"$V2/parity-old.log" 2>&1 &
OLD_PID=$!
GRIDWELL_HOME="$V2" "$GW" serve --bind "127.0.0.1:$PORT_B" >"$V2/parity-new.log" 2>&1 &
NEW_PID=$!
sleep 5

if ! "$GW" parity --a "http://127.0.0.1:$PORT_A" --b "http://127.0.0.1:$PORT_B" \
    ${PW_ARGS[@]+"${PW_ARGS[@]}"} --scope "$V2/convert-scope.txt" \
    --ignore-fields status_detail,stale,menu_entries | grep -vE '^skipped'; then
  echo
  echo "migrate-v2: PARITY FAILED — NOTHING was swapped."
  echo "  old home untouched: $HOME_DIR"
  echo "  converted home kept for inspection: $V2 (logs: parity-old.log, parity-new.log)"
  exit 1
fi
cleanup
trap - EXIT

# 5. Swap. The old home is the rollback until your first write in v2.
echo "== swap"
mv "$HOME_DIR" "$BACKUPS/v1-home-$NAME-$STAMP"
mv "$V2" "$HOME_DIR"
rm -f "$HOME_DIR/parity-old.log" "$HOME_DIR/parity-new.log" "$HOME_DIR/serve.lock"

echo
echo "migrate-v2: DONE — $HOME_DIR is the v2 home."
echo "  rollback: stop the server, then"
echo "    mv $HOME_DIR ${HOME_DIR}-v2-failed && mv $BACKUPS/v1-home-$NAME-$STAMP $HOME_DIR"
echo "  restart your server, then verify by hand. Optional health crawl:"
echo "    $GW parity --a http://127.0.0.1:<port> --b http://127.0.0.1:<port> \\"
echo "        ${PW_ARGS[*]:-} --scope $HOME_DIR/convert-scope.txt --ignore-fields status_detail,stale,menu_entries"
