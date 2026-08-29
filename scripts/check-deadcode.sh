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

NATIVE_ROOTS=(./apps/gridwell ./plugins/fs/cmd/... ./plugins/proc/cmd/... ./plugins/gitlab/cmd/...)
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

# An allowlist entry is a claim ("this symbol is unrooted but shipped");
# once the symbol is deleted or gains a root the claim is stale and the
# entry hides the next real finding under it (parity.Crawl, goal sweep
# 2026-08-27). Every entry must still name a symbol deadcode reports.
if [ -f "$allow" ]; then
	while IFS= read -r line; do
		case "$line" in ''|\#*) continue ;; esac
		case "$line" in *' '*.*) continue ;; esac # "<file> <Type>.<Method>" lines belong to the pass below
		grep -qxF "$line" <<<"$native" || { echo "stale allowlist entry (no longer dead, or gone): $line" >&2; flagged=1; }
	done <"$allow"
	[ "$flagged" = 0 ] || exit 1
fi

# Exported METHODS: RTA keeps a method alive whenever its signature
# satisfies an interface the program calls dynamically, so a method with
# no caller at all (Server.Handler, Plugin.Store, Registry.IDs — used only
# by tests, 2026-08-27) never shows above. Coarse textual pass: an
# exported method whose name is never written as ".Name(" in any non-test
# Go file (generated code included — that is how RPC services are called)
# has no caller. Allowlist lines "<file> <Type>.<Method>" cover the rest.
uncalled=()
called=$(git ls-files -z -- '*.go' | grep -zv '_test\.go$' | xargs -0 grep -hoE '\.[A-Z][A-Za-z0-9_]*\(' | LC_ALL=C sort -u)
flagged=0
while IFS=: read -r file _ decl; do
	case "$file" in *_test.go|*.pb.go|*.connect.go|*test/*) continue ;; esac
	name=$(sed -E 's/^func \([^)]*\) ([A-Z][A-Za-z0-9_]*)\(.*/\1/' <<<"$decl")
	typ=$(sed -E 's/^func \(([^)]*)\).*/\1/' <<<"$decl" | awk '{print $NF}' | sed -E 's/^\*//; s/\[.*//')
	grep -qxF ".$name(" <<<"$called" && continue
	entry="$file $typ.$name"
	uncalled+=("$entry")
	[ -f "$allow" ] && grep -qxF "$entry" "$allow" && continue
	[ "$flagged" = 0 ] && echo "exported methods with no caller outside tests:" >&2
	echo "  $entry" >&2
	flagged=1
done < <(git ls-files -z -- 'internal/*.go' 'api/*.go' 'client/*.go' 'plugins/*.go' | xargs -0 grep -nE '^func \([^)]*\) [A-Z][A-Za-z0-9_]*\(' 2>/dev/null)
if [ "$flagged" != 0 ]; then
	echo "delete it, or allowlist it in $allow as \"<file> <Type>.<Method>\" with a reason." >&2
	exit 1
fi
# Method allowances go stale the same way.
if [ -f "$allow" ]; then
	while IFS= read -r line; do
		case "$line" in ''|\#*) continue ;; esac
		case "$line" in *' '*.*) ;; *) continue ;; esac
		printf '%s\n' "${uncalled[@]}" | grep -qxF "$line" || { echo "stale allowlist entry (the method has a caller now, or is gone): $line" >&2; flagged=1; }
	done <"$allow"
	[ "$flagged" = 0 ] || exit 1
fi
echo "deadcode: clean"
