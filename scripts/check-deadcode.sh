#!/usr/bin/env bash
# check-deadcode: every first-party function must be reachable from a SHIPPED
# binary — the native binaries (server, plugins, converters) or the wasm
# client. Reachable only from a test does not count: production code kept
# alive by its own test is still dead (the TaskCount/NodeIdentity class,
# goal sweep 2026-08-24). The previous sweep ran deadcode BY HAND and the
# findings grew back within weeks; this gate is the fix for that class.
#
# Two worlds, one verdict: a symbol is dead when the native world cannot
# reach it AND either its package is not part of the wasm build or the wasm
# world cannot reach it either. Packages named *test (shellsvctest) exist
# for tests and are exempt. Deliberate exceptions go in
# scripts/deadcode-allow.txt as "<file> <func>" lines with a comment.
set -euo pipefail
cd "$(dirname "$0")/.."

NATIVE_ROOTS=(./apps/gridwell ./apps/gridwell-all ./plugins/fs/cmd/... ./plugins/proc/cmd/...)
PKGS=(./internal/... ./api/... ./client/...)

# "path/file.go:12:6: unreachable func: Name" -> "path/file.go Name"
norm() { sed 's/:[0-9][0-9]*:[0-9][0-9]*: unreachable func: / /' | LC_ALL=C sort -u; }

# Resolve the tool to a NATIVE binary first: `go tool` under GOOS=js would
# try to build (and exec) a wasm deadcode.
DEADCODE=$(go tool -n deadcode)
native=$("$DEADCODE" "${NATIVE_ROOTS[@]}" "${PKGS[@]}" | norm)
wasm=$(GOOS=js GOARCH=wasm "$DEADCODE" ./client/wasm | norm)
wasmdirs=$(GOOS=js GOARCH=wasm go list -deps -f '{{.Dir}}' ./client/wasm | sed "s|^$PWD/||")

allow=scripts/deadcode-allow.txt
flagged=0
while IFS= read -r line; do
	[ -z "$line" ] && continue
	file=${line%% *}
	dir=$(dirname "$file")
	case "$dir" in */*test) continue ;; esac
	if grep -qxF "$dir" <<<"$wasmdirs"; then
		grep -qxF "$line" <<<"$wasm" || continue
	fi
	if [ -f "$allow" ] && grep -qF "$line" "$allow"; then continue; fi
	if [ "$flagged" = 0 ]; then
		echo "dead code — unreachable from every shipped binary (tests don't count):" >&2
	fi
	echo "  $line" >&2
	flagged=1
done <<<"$native"
if [ "$flagged" != 0 ]; then
	echo "delete it, or allowlist it in $allow with a reason." >&2
	exit 1
fi
echo "deadcode: clean"
