#!/usr/bin/env bash
# check-exception-owners: an exception the user can see is asked once, by
# name. Each of serves_page, text_presentation, host_content, stale, the link
# target and the link bit is a fact a plugin or a source declares, and
# each has one predicate that turns it into the question the rest of the
# client asks. A bare field read outside that owner is a second derivation,
# and two derivations drift.
#
# Each line of scripts/exception-owners.txt is "<field regex> <owner path
# regex>", and the field may be read only in paths matching the regex. The
# scope is the client and the wire types it reads. A _test.go file is exempt,
# because a test may build any shape it likes, and so is
# client/wasm/testhook.go, which reports fields to the e2e harness and decides
# nothing. A comment is not a read, and neither is a "kind:" key in a
# composite literal: that tags an outgoing CreateTile or SetTile with the arm
# it wants. Only a comparison asks a question.
set -euo pipefail
cd "$(dirname "$0")/.."

files=$(git ls-files -- 'client/**/*.go' 'api/rpc/**/*.go' \
  | grep -v '_test\.go$' | grep -v '^client/wasm/testhook\.go$')

bad=0
while read -r pat allow; do
  [ -z "$pat" ] && continue
  case "$pat" in \#*) continue ;; esac
  # Strip whole-line comments before matching: only code counts as a read.
  hits=$(printf '%s\n' "$files" | xargs grep -n -- "$pat" \
    | grep -vE '^[^:]+:[0-9]+:[[:space:]]*//' \
    | grep -vE 'Kind:[[:space:]]*rpc\.Kind' \
    | grep -vE "^($allow)" || true)
  if [ -n "$hits" ]; then
    echo "exception field read outside its owner ($pat):"
    printf '%s\n' "$hits"
    bad=1
  fi
done < scripts/exception-owners.txt

if [ $bad != 0 ]; then
  echo "route the question through its predicate, or record the owner in scripts/exception-owners.txt with a reason"
  exit 1
fi
echo "exception owners: clean"
