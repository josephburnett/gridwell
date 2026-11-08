#!/usr/bin/env bash
# check-vocabulary: retired words do not come back. A rename left to intention
# erodes, because a comment keeps the old word and the next reader copies it.
# Each line of scripts/retired-words.txt is "<word> [<allowed path regex>]",
# and the word, whole and case-insensitive, may appear only in paths matching
# the regex, such as a migration shim that must still spell the old name.
set -euo pipefail
cd "$(dirname "$0")/.."

bad=0
while read -r word allow; do
  [ -z "$word" ] && continue
  case "$word" in \#*) continue ;; esac
  hits=$(git ls-files -z -- '*.go' '*.ts' '*.md' '*.yml' '*.yaml' '*.sh' '*.proto' '*.mod' 'Makefile' \
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
