#!/usr/bin/env bash
# check-vocabulary: retired words do not come back. A rename decided by the
# owner ("one word: plugin", 2026-08-27; kinds local→home) erodes when it
# is left to intention — comments keep the old word, the next reader copies
# it, and the halfway world returns. Each line of scripts/retired-words.txt
# is "<word> [<allowed path regex>]": the word (whole-word, case-insensitive)
# may appear only in paths matching the regex (historical design records,
# the migration shim that must spell the old name).
set -euo pipefail
cd "$(dirname "$0")/.."

bad=0
while read -r word allow; do
  [ -z "$word" ] && continue
  case "$word" in \#*) continue ;; esac
  hits=$(git ls-files -z -- '*.go' '*.ts' '*.md' '*.yml' '*.yaml' '*.sh' '*.proto' 'Makefile' \
    | xargs -0 grep -nwi -- "$word" 2>/dev/null \
    | grep -v '\.pb\.go:\|\.connect\.go:' || true)
  if [ -n "$allow" ]; then
    hits=$(printf '%s\n' "$hits" | grep -Ev "^($allow)" || true)
  fi
  if [ -n "$hits" ]; then
    echo "retired word \"$word\":"
    printf '%s\n' "$hits"
    bad=1
  fi
done < scripts/retired-words.txt
[ $bad = 0 ] && echo "vocabulary: clean"
exit $bad
