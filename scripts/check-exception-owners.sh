#!/usr/bin/env bash
# check-exception-owners: an exception the user can see is asked once, by
# name. serves_page, text_presentation, host_content, stale, the link target,
# the reference bit — each is a fact a plugin or a source declares, and each
# has one predicate that turns it into a question the rest of the client asks
# ("is this the document", "is this a link", "does this grid project host
# state"). A bare field read outside that owner is a second derivation, and
# two derivations drift: the url-vs-page question was spelled three ways
# before this gate existed.
#
# Each line of scripts/exception-owners.txt is "<field regex> <owner path
# regex>": the field may be read only in paths matching the regex. Scope is
# the client and the wire types it reads (client/, api/rpc/). Exempt by path:
# _test.go (a test may build any shape it likes) and client/wasm/testhook.go,
# which reports fields to the e2e harness and decides nothing. Comment lines
# are not reads — prose naming a field is how the owners explain themselves.
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
