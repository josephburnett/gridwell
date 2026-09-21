#!/usr/bin/env bash
# gates-named: a shim commit says which gate saw it. `make check` compiles
# client/wasm and web/ and executes neither, so for a commit that touches them
# the message is the only record that anything ran the code. This reports and
# never fails: a name in a message is a claim, and the gate itself is the
# proof.
set -euo pipefail
cd "$(dirname "$0")/.."

range="${1:-}"
if [ -z "$range" ]; then
  echo "usage: scripts/gates-named.sh <commit range>   # e.g. origin/main..HEAD" >&2
  exit 2
fi
if ! git rev-list --no-merges "$range" >/dev/null 2>&1; then
  echo "gates-named: $range does not resolve here — a shallow clone carries no history to read"
  exit 0
fi

unnamed=0
for sha in $(git rev-list --no-merges "$range"); do
  if ! git show --name-only --format= "$sha" | grep -Eq '^(client/wasm/|web/)'; then
    continue
  fi
  if git log -1 --format=%B "$sha" | grep -Eq 'check-(e2e|web|electron)'; then
    continue
  fi
  echo "names no gate: $(git log -1 --format='%h %s' "$sha")"
  unnamed=$((unnamed + 1))
done

if [ "$unnamed" = 0 ]; then
  echo "gates-named: every shim commit in $range names a gate"
else
  echo "gates-named: $unnamed shim commit(s) in $range name no gate — see CLAUDE.md's Gates table"
fi
