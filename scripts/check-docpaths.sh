#!/usr/bin/env bash
# check-docpaths: every repo path a document names must exist. Docs cite
# code by path (`internal/local/store`, `plugins/fs`, `apps/desktop/...`)
# and those paths rot silently when code moves — ARCHITECTURE.md kept
# describing `internal/plugin/sshhost` (never existed after the rename)
# and three docs cited `plugins/local/store` months after the v2 fold
# made it `internal/local/store`. A stale path is a false fact about
# where something lives, and nothing checked it. This gate does.
#
# Scope: every tracked *.md plus .github/workflows/*.yml. A token is
# path-like when it starts with one of the top-level code dirs
# (internal/ plugins/ api/ client/ apps/ scripts/ test/ web/ mobile/)
# and continues with path characters; a trailing `/` or `/...` is fine,
# trailing punctuation and a `.GoSymbol` suffix (`internal/dbformat.
# EnsureVersion`) are stripped. Each must satisfy `test -e` from the
# repo root.
#
# Exemptions go in scripts/docpaths-allow.txt as "<file> <path>" lines
# with a comment saying why the path is gone.
set -euo pipefail
cd "$(dirname "$0")/.."

allow=scripts/docpaths-allow.txt
skip_files=()

files=$(git ls-files '*.md' '.github/workflows/*.yml' | grep -v '/node_modules/')
bad=0
for f in $files; do
  for s in "${skip_files[@]}"; do [ "$f" = "$s" ] && continue 2; done
  # Path-like tokens; the leading dir must sit on a word boundary so
  # `mobile/` inside `apps/mobile/` (say) is not split in two.
  for p in $(grep -oE '(^|[^A-Za-z0-9_./-])(internal|plugins|api|client|apps|scripts|test|web|mobile)/[A-Za-z0-9_./*-]*' "$f" \
      | sed -E 's/^[^A-Za-z]//' | sed -E 's#/\.\.\.$##; s#[.,;:)*]+$##; s#\.[A-Z][A-Za-z0-9]*$##; s#/$##' | LC_ALL=C sort -u); do
    [ -z "$p" ] && continue
    if [ -e "$p" ]; then continue; fi
    if grep -qE "^$f $p( |$)" "$allow" 2>/dev/null; then continue; fi
    echo "$f: path does not exist: $p"
    bad=1
  done
done
if [ "$bad" != 0 ]; then
  echo "docs cite paths that no longer exist — fix the doc, or record a deliberately historical mention in $allow"
  exit 1
fi
echo "check-docpaths: ok"
